package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/noosxe/runnero/internal/db"
	supervisorv1 "github.com/noosxe/runnero/internal/pb/supervisor/v1"
	"github.com/noosxe/runnero/internal/pb/supervisor/v1/supervisorv1connect"
	"golang.org/x/crypto/bcrypt"
)

const (
	// SessionCookieName is the HttpOnly cookie carrying the opaque session
	// token (docs/05 §5, docs/32 §3.4).
	SessionCookieName = "session_token"

	// OpaqueTokenBytes is the entropy of a session token: 32 bytes from
	// crypto/rand, hex-encoded on the wire (docs/32 §3.2). Only its
	// SHA-256 hash is stored server-side.
	OpaqueTokenBytes = 32
)

// Auth audit action vocabulary (docs/32 §5.1). Reasons stay coarse
// ("invalid_credentials"): the historical user_not_found/invalid_password
// split is exactly what must not be distinguishable from the outside.
const (
	ActionAuthLoginSuccess   = "auth.login_success"
	ActionAuthLoginFailed    = "auth.login_failed"
	ActionAuthLogout         = "auth.logout"
	ActionAuthSessionRevoked = "auth.session_revoked"
	ActionAuthSetupAdmin     = "auth.setup_admin"
	ActionAuthRateLimited    = "auth.rate_limited"
	ActionAuthAccessDenied   = "auth.access_denied"
)

// roleBucket classifies a Connect procedure for the fail-closed enforcement
// matrix (docs/32 §2.3). bucketViewer is reserved for a future observer-users
// feature; no procedure maps to it and no code path assigns the role yet.
type roleBucket int

const (
	bucketPublic roleBucket = iota
	bucketViewer
	bucketAdmin
)

// procedureRoles is the static procedure-to-role matrix — the single
// enforcement point for authentication and authorization (docs/32 §2.1,
// §2.3). Rules:
//
//   - every procedure in proto/api.proto MUST be classified exactly once;
//     the coverage test (auth_matrix_test.go) asserts this against the
//     generated protobuf file descriptor, so a new RPC cannot ship
//     unclassified;
//   - an unclassified procedure is rejected with CodeInternal at request
//     time (fail closed);
//   - bucketAdmin requires the calling user to carry the admin role.
var procedureRoles = map[string]roleBucket{
	// Public: bootstrap, login, and the pre-auth status probe. Public
	// procedures still upgrade their context with the user when a valid
	// session cookie is present (docs/32 §3.3).
	supervisorv1connect.AuthServiceSetupAdminProcedure:                bucketPublic,
	supervisorv1connect.AuthServiceLoginProcedure:                     bucketPublic,
	supervisorv1connect.OnboardingServiceGetOnboardingStatusProcedure: bucketPublic,

	// Admin: the bootstrap admin is the only user that exists today, so
	// every remaining procedure — reads included — shares the admin bucket
	// (docs/32 §2.3).
	supervisorv1connect.AuthServiceGetSessionProcedure: bucketAdmin,

	// Session control (docs/32 §3.5): the caller manages only their own
	// rows; ownership is enforced in SQL, the bucket gates the surface.
	supervisorv1connect.AuthServiceLogoutProcedure:                    bucketAdmin,
	supervisorv1connect.AuthServiceListSessionsProcedure:              bucketAdmin,
	supervisorv1connect.AuthServiceRevokeSessionProcedure:             bucketAdmin,
	supervisorv1connect.AuthServiceRevokeOtherSessionsProcedure:       bucketAdmin,
	supervisorv1connect.OnboardingServiceCompleteOnboardingProcedure:  bucketAdmin,
	supervisorv1connect.OnboardingServiceGetAppSettingsProcedure:      bucketAdmin,
	supervisorv1connect.OnboardingServiceSetAppSettingProcedure:       bucketAdmin,
	supervisorv1connect.PoolServiceListPoolsProcedure:                 bucketAdmin,
	supervisorv1connect.PoolServiceCreatePoolProcedure:                bucketAdmin,
	supervisorv1connect.PoolServiceUpdatePoolProcedure:                bucketAdmin,
	supervisorv1connect.PoolServiceDeletePoolProcedure:                bucketAdmin,
	supervisorv1connect.PoolServiceWatchPoolsProcedure:                bucketAdmin,
	supervisorv1connect.PoolServiceListRunnersProcedure:               bucketAdmin,
	supervisorv1connect.PoolServiceWatchRunnersProcedure:              bucketAdmin,
	supervisorv1connect.PoolServiceTerminateRunnerProcedure:           bucketAdmin,
	supervisorv1connect.PoolServiceDiscoverTargetsProcedure:           bucketAdmin,
	supervisorv1connect.AuthProfileServiceListAuthProfilesProcedure:   bucketAdmin,
	supervisorv1connect.AuthProfileServiceCreateAuthProfileProcedure:  bucketAdmin,
	supervisorv1connect.AuthProfileServiceUpdateAuthProfileProcedure:  bucketAdmin,
	supervisorv1connect.AuthProfileServiceDeleteAuthProfileProcedure:  bucketAdmin,
	supervisorv1connect.AnalyticsServiceGetJobHistoryProcedure:        bucketAdmin,
	supervisorv1connect.AnalyticsServiceGetJobRecordProcedure:         bucketAdmin,
	supervisorv1connect.AnalyticsServiceGetSystemStatsProcedure:       bucketAdmin,
	supervisorv1connect.AnalyticsServiceWatchDashboardProcedure:       bucketAdmin,
	supervisorv1connect.ImageUpdateServiceListImageUpdatesProcedure:   bucketAdmin,
	supervisorv1connect.ImageUpdateServiceCheckImageUpdateProcedure:   bucketAdmin,
	supervisorv1connect.ImageUpdateServicePullImageProcedure:          bucketAdmin,
	supervisorv1connect.ImageUpdateServiceDismissImageUpdateProcedure: bucketAdmin,
	supervisorv1connect.LogServiceGetRunnerLogsProcedure:              bucketAdmin,
	supervisorv1connect.LogServiceStreamRunnerLogsProcedure:           bucketAdmin,
	supervisorv1connect.LogServiceListSupervisorLogsProcedure:         bucketAdmin,
	supervisorv1connect.LogServiceStreamSupervisorLogProcedure:        bucketAdmin,
	supervisorv1connect.LogServiceListRemovalRecordsProcedure:         bucketAdmin,
	supervisorv1connect.RenovateServiceGetRenovateStatusProcedure:     bucketAdmin,
	supervisorv1connect.RenovateServiceTriggerRenovateRunProcedure:    bucketAdmin,
	supervisorv1connect.RenovateServiceListRenovateHistoryProcedure:   bucketAdmin,
}

// SessionConfig carries the validated web-session knobs (docs/32 §3, §6).
type SessionConfig struct {
	// IdleTimeout is the sliding idle deadline: sessions that see no
	// activity for this long are deleted (docs/32 §3.1).
	IdleTimeout time.Duration
	// AbsoluteTimeout is the fixed lifetime cap fixed at issuance and
	// never extended; it bounds every stolen-cookie scenario.
	AbsoluteTimeout time.Duration
	// SecureMode is one of config.SecureCookieMode* (auto/always/never).
	SecureMode string
	// BcryptCost is the hashing cost for new/changed passwords (4-31).
	BcryptCost int
	// TrustedProxy enables trusting X-Forwarded-For / X-Forwarded-Proto
	// for client-IP extraction and Secure=auto detection.
	TrustedProxy bool
}

// AuthDatabase defines the database subset required by the authentication engine and interceptor.
// *db.DB satisfies this interface directly.
type AuthDatabase interface {
	CountAdminUsers(ctx context.Context) (int64, error)
	CreateAdminUser(ctx context.Context, arg db.CreateAdminUserParams) (db.AdminUser, error)
	GetAdminUserByUsername(ctx context.Context, username string) (db.AdminUser, error)
	GetAdminUserById(ctx context.Context, id int64) (db.AdminUser, error)
	CreateSession(ctx context.Context, arg db.CreateSessionParams) (db.Session, error)
	GetSessionByTokenHash(ctx context.Context, tokenHash string) (db.Session, error)
	TouchSession(ctx context.Context, arg db.TouchSessionParams) error
	DeleteSessionByTokenHash(ctx context.Context, tokenHash string) error
	ListSessionsByUserId(ctx context.Context, userID int64) ([]db.Session, error)
	DeleteSessionByIdAndUserId(ctx context.Context, arg db.DeleteSessionByIdAndUserIdParams) (int64, error)
	DeleteOtherSessionsByUserId(ctx context.Context, arg db.DeleteOtherSessionsByUserIdParams) (int64, error)
	CreateAuditLog(ctx context.Context, arg db.CreateAuditLogParams) (db.AuditLog, error)
}

type contextKey string

const (
	userContextKey contextKey = "supervisor.auth.user"
	requestInfoKey contextKey = "supervisor.request.info"
)

// UserContext carries authenticated administrator details across the request lifecycle.
type UserContext struct {
	UserID    int64
	Username  string
	Role      string
	SessionID int64
}

// WithUserContext embeds authenticated UserContext into the request context.
func WithUserContext(ctx context.Context, u *UserContext) context.Context {
	return context.WithValue(ctx, userContextKey, u)
}

// GetUserContext retrieves the authenticated UserContext from the context if present.
func GetUserContext(ctx context.Context) (*UserContext, bool) {
	u, ok := ctx.Value(userContextKey).(*UserContext)
	return u, ok
}

// requestInfo carries per-request transport facts resolved once by the
// request-info middleware (docs/32 §3.4, §4.1): the client IP (honoring
// X-Forwarded-For only behind a trusted proxy) and whether the request
// arrived over HTTPS (direct TLS or forwarded proto behind a trusted proxy).
type requestInfo struct {
	ClientIP string
	Secure   bool
}

func withRequestInfo(ctx context.Context, info *requestInfo) context.Context {
	return context.WithValue(ctx, requestInfoKey, info)
}

func requestInfoFromContext(ctx context.Context) (*requestInfo, bool) {
	info, ok := ctx.Value(requestInfoKey).(*requestInfo)
	return info, ok
}

// recordAuthAudit writes an auth decision to audit_logs with the resolved
// client IP (docs/32 §5.1). resource_type is always admin_user; resource_id
// mirrors the acting user when known. details must never carry credentials,
// tokens, or challenge bytes (asserted by the leakage tests).
func recordAuthAudit(ctx context.Context, database AuditLogDatabase, userID *int64, action, sourceIP string, details map[string]any) {
	if database == nil {
		return
	}
	var uid sql.NullInt64
	if userID != nil && *userID > 0 {
		uid = sql.NullInt64{Int64: *userID, Valid: true}
	}
	var detailsJSON sql.NullString
	if details != nil {
		if b, err := json.Marshal(details); err == nil {
			detailsJSON = sql.NullString{String: string(b), Valid: true}
		}
	}
	_, _ = database.CreateAuditLog(ctx, db.CreateAuditLogParams{
		UserID:       uid,
		Action:       action,
		ResourceType: sql.NullString{String: "admin_user", Valid: true},
		ResourceID:   uid,
		Details:      detailsJSON,
		SourceIp:     sql.NullString{String: sourceIP, Valid: sourceIP != ""},
	})
}

// authClientIP resolves the request's client IP from the request-info
// middleware for audit and rate-limit keys (docs/32 §4.1). Absent middleware
// (tests, direct service construction) yields "".
func authClientIP(ctx context.Context) string {
	if info, ok := requestInfoFromContext(ctx); ok {
		return info.ClientIP
	}
	return ""
}

// HashToken computes the deterministic SHA-256 hex string of a raw token string (OQ #11).
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// GenerateOpaqueToken creates a new 32-byte crypto/rand session token,
// hex-encoded (docs/32 §3.2). The value appears only in the cookie; the
// database stores its SHA-256 hash.
func GenerateOpaqueToken() (string, error) {
	raw := make([]byte, OpaqueTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating session token: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

// ExtractSessionToken extracts the session token from the HttpOnly cookie.
// The Authorization: Bearer fallback was removed with the opaque-session
// redesign: the cookie is the only credential (docs/32 §2.5).
func ExtractSessionToken(header http.Header) string {
	r := &http.Request{Header: header}
	if cookie, err := r.Cookie(SessionCookieName); err == nil && cookie.Value != "" {
		return cookie.Value
	}
	return ""
}

// secureForRequest resolves the cookie Secure attribute for a request
// (docs/32 §3.4): always and never pin it; auto attaches it only when the
// request arrived over HTTPS (direct TLS, or X-Forwarded-Proto: https
// behind a configured trusted proxy).
func (c SessionConfig) secureForRequest(info *requestInfo) bool {
	switch c.SecureMode {
	case "always":
		return true
	case "never":
		return false
	default: // "auto" (and any validated-config default)
		return info != nil && info.Secure
	}
}

// sessionCookie builds the session cookie (docs/32 §3.4): Path=/, HttpOnly,
// SameSite=Strict (the deliberate deviation from the guide's Lax — docs/32
// §2.2), Max-Age tracking the idle deadline so browser expiry tracks the
// server row, Secure per the resolved mode. A maxAge of 0 with an empty
// token produces the logout/deletion cookie.
func (c SessionConfig) sessionCookie(token string, maxAge int, secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	}
}

// AuthInterceptor enforces valid cookie session authentication on protected procedures (unary and streaming).
type AuthInterceptor struct {
	authDB AuthDatabase
	cfg    SessionConfig
}

// NewAuthInterceptor returns a Connect Interceptor enforcing valid cookie session authentication
// on all protected procedures (both unary and streaming), with the fail-closed
// procedure-to-role matrix (docs/32 §2.3).
func NewAuthInterceptor(authDB AuthDatabase, cfg SessionConfig) connect.Interceptor {
	return &AuthInterceptor{
		authDB: authDB,
		cfg:    cfg,
	}
}

// authenticate validates the request's session cookie, enforces the role
// matrix, and returns the authenticated context plus an optional Set-Cookie
// value to attach to the response (sliding renewal — docs/32 §3.3).
func (a *AuthInterceptor) authenticate(ctx context.Context, header http.Header, procedure string) (context.Context, string, error) {
	bucket, classified := procedureRoles[procedure]
	if !classified {
		// Fail closed: an unclassified procedure is a build/ship bug
		// (docs/32 §2.3); the coverage test keeps this unreachable.
		return nil, "", connect.NewError(connect.CodeInternal, fmt.Errorf("procedure %q is not classified in the role matrix", procedure))
	}

	info, _ := requestInfoFromContext(ctx)
	tokenString := ExtractSessionToken(header)
	if tokenString == "" {
		if bucket == bucketPublic {
			// Public procedure without a cookie: anonymous access.
			return ctx, "", nil
		}
		return nil, "", connect.NewError(connect.CodeUnauthenticated, errors.New("unauthorized: missing session token"))
	}

	// A cookie is present: validate it (two clocks + user lookup), slide
	// the idle deadline when past the half-window, and upgrade even public
	// procedures with the authenticated context (docs/32 §3.3).
	sess, user, renewalCookie, err := a.validateSession(ctx, tokenString, info)
	if err != nil {
		return nil, "", err
	}

	userCtx := &UserContext{
		UserID:    user.ID,
		Username:  user.Username,
		Role:      user.Role,
		SessionID: sess.ID,
	}

	if bucket == bucketAdmin && user.Role != "admin" {
		recordAuthAudit(ctx, a.authDB, &user.ID, ActionAuthAccessDenied, authClientIP(ctx), map[string]any{
			"username":  user.Username,
			"procedure": procedure,
		})
		return nil, "", connect.NewError(connect.CodePermissionDenied, errors.New("admin role required"))
	}

	return WithUserContext(ctx, userCtx), renewalCookie, nil
}

// validateSession resolves a session token hash to a live session and user,
// deleting rows that expired by either clock, and sliding the idle deadline
// once the session is past half of its idle window — clamped at the absolute
// cap, which is never extended (docs/32 §3.3). The returned cookie value
// (empty when no renewal happened) refreshes the browser-side Max-Age.
func (a *AuthInterceptor) validateSession(ctx context.Context, tokenString string, info *requestInfo) (db.Session, db.AdminUser, string, error) {
	now := time.Now()
	tokenHash := HashToken(tokenString)

	sess, err := a.authDB.GetSessionByTokenHash(ctx, tokenHash)
	if err != nil {
		return db.Session{}, db.AdminUser{}, "", connect.NewError(connect.CodeUnauthenticated, errors.New("unauthorized: unknown session"))
	}
	if now.After(sess.AbsoluteExpiresAt) {
		_ = a.authDB.DeleteSessionByTokenHash(ctx, tokenHash)
		return db.Session{}, db.AdminUser{}, "", connect.NewError(connect.CodeUnauthenticated, errors.New("unauthorized: session expired"))
	}
	if now.After(sess.ExpiresAt) {
		_ = a.authDB.DeleteSessionByTokenHash(ctx, tokenHash)
		return db.Session{}, db.AdminUser{}, "", connect.NewError(connect.CodeUnauthenticated, errors.New("unauthorized: session expired"))
	}

	user, err := a.authDB.GetAdminUserById(ctx, sess.UserID)
	if err != nil {
		return db.Session{}, db.AdminUser{}, "", connect.NewError(connect.CodeUnauthenticated, errors.New("unauthorized: unknown user"))
	}

	// Sliding renewal: only slide once past half of the idle window so
	// activity does not write on every request; clamp at the absolute cap.
	setCookie := ""
	if half := a.cfg.IdleTimeout / 2; now.After(sess.ExpiresAt.Add(-half)) {
		newIdle := now.Add(a.cfg.IdleTimeout)
		if newIdle.After(sess.AbsoluteExpiresAt) {
			newIdle = sess.AbsoluteExpiresAt
		}
		if err := a.authDB.TouchSession(ctx, db.TouchSessionParams{ExpiresAt: newIdle, TokenHash: tokenHash}); err == nil {
			secure := a.cfg.secureForRequest(info)
			setCookie = a.cfg.sessionCookie(tokenString, int(a.cfg.IdleTimeout.Seconds()), secure).String()
		}
		// A failed touch leaves the session valid at its old deadline;
		// log volume aside, nothing breaks — the next request retries.
	}

	return sess, user, setCookie, nil
}

func (a *AuthInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		authCtx, setCookie, err := a.authenticate(ctx, req.Header(), req.Spec().Procedure)
		if err != nil {
			return nil, err
		}
		resp, err := next(authCtx, req)
		if err == nil && setCookie != "" {
			resp.Header().Add("Set-Cookie", setCookie)
		}
		return resp, err
	}
}

func (a *AuthInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (a *AuthInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		authCtx, setCookie, err := a.authenticate(ctx, conn.RequestHeader(), conn.Spec().Procedure)
		if err != nil {
			return err
		}
		if setCookie != "" {
			// Response headers must be set before the handler sends;
			// streaming responses write them with the first Send.
			conn.ResponseHeader().Add("Set-Cookie", setCookie)
		}
		return next(authCtx, conn)
	}
}

// AuthService implements supervisorv1connect.AuthServiceHandler.
type AuthService struct {
	supervisorv1connect.UnimplementedAuthServiceHandler
	db      AuthDatabase
	cfg     SessionConfig
	limiter *loginRateLimiter

	// dummyOnce guards the one-time generation of dummyHash, which is
	// compared against on the unknown-username path so both login failures
	// run one bcrypt comparison and the response timing cannot reveal
	// whether an account exists (docs/32 §4.3).
	dummyOnce sync.Once
	dummyHash []byte
}

// NewAuthService constructs an AuthService instance.
func NewAuthService(authDB AuthDatabase, cfg SessionConfig) *AuthService {
	s := &AuthService{
		db:      authDB,
		cfg:     cfg,
		limiter: newLoginRateLimiter(),
	}
	// Prewarm the equalization hash off the boot path: a cost-12 bcrypt
	// takes ~150ms normally but seconds under the race detector, and it
	// must never delay daemon startup. The login path waits on the same
	// sync.Once, so the first unknown-username login reuses the finished
	// hash or completes the prewarm itself - always the same hash.
	go s.dummyHashBytes()
	return s
}

// dummyHashBytes returns the startup-generated bcrypt hash used to equalize
// the unknown-username login path. Generation happens once (off the boot
// path, see NewAuthService); if it fails - invalid cost is rejected by
// config validation, so this is defense in depth - the fallback is a fixed
// cost-12 hash of an unguessable random value, still a valid bcrypt hash to
// compare against.
func (s *AuthService) dummyHashBytes() []byte {
	s.dummyOnce.Do(func() {
		dummyPwd, terr := GenerateOpaqueToken()
		if terr != nil {
			dummyPwd = "runnero-timing-equalizer-password"
		}
		dummy, err := bcrypt.GenerateFromPassword([]byte(dummyPwd), s.cfg.BcryptCost)
		if err != nil {
			dummy = []byte("$2a$12$C6UzMDM.H6dfI/f/IKcEeO7ZUBmSrHVpTGuBOqFvECCQ1cWRTfnU6")
		}
		s.dummyHash = dummy
	})
	return s.dummyHash
}

// secureFor resolves the request-scoped Secure attribute for cookies issued
// by this service (docs/32 §3.4).
func (s *AuthService) secureFor(ctx context.Context) bool {
	info, _ := requestInfoFromContext(ctx)
	return s.cfg.secureForRequest(info)
}

// issueSession is the single session-issuance path every login method
// converges on (docs/32 §3.2): an opaque 32-byte crypto/rand token whose
// SHA-256 hash is stored with both clock deadlines, plus the matching
// Set-Cookie header value with Max-Age tracking the idle deadline.
func (s *AuthService) issueSession(ctx context.Context, user db.AdminUser, userAgent string) (string, error) {
	tokenString, err := GenerateOpaqueToken()
	if err != nil {
		return "", err
	}

	now := time.Now()
	_, err = s.db.CreateSession(ctx, db.CreateSessionParams{
		LastSeenAt:        now,
		UserID:            user.ID,
		TokenHash:         HashToken(tokenString),
		ExpiresAt:         now.Add(s.cfg.IdleTimeout),
		AbsoluteExpiresAt: now.Add(s.cfg.AbsoluteTimeout),
		UserAgent:         userAgent,
	})
	if err != nil {
		return "", err
	}

	secure := s.secureFor(ctx)
	cookie := s.cfg.sessionCookie(tokenString, int(s.cfg.IdleTimeout.Seconds()), secure)
	return cookie.String(), nil
}

// SetupAdmin creates the initial local administrator account. Fails if an administrator already exists.
func (s *AuthService) SetupAdmin(ctx context.Context, req *connect.Request[supervisorv1.SetupAdminRequest]) (*connect.Response[supervisorv1.SetupAdminResponse], error) {
	username := strings.TrimSpace(req.Msg.Username)
	password := req.Msg.Password

	// Trim-aware username check: min_len sees the raw wire string, but the
	// account is stored with the trimmed value (password emptiness is class A).
	if username == "" {
		return nil, invalidArgument(newViolation(RuleAuthUsernameRequired, "username", "username must not be empty"))
	}

	count, err := s.db.CountAdminUsers(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("checking existing admin users: %w", err))
	}
	if count > 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("administrator already configured"))
	}

	hashBytes, err := bcrypt.GenerateFromPassword([]byte(password), s.cfg.BcryptCost)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("hashing password: %w", err))
	}

	user, err := s.db.CreateAdminUser(ctx, db.CreateAdminUserParams{
		Username:     username,
		PasswordHash: string(hashBytes),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("creating admin user: %w", err))
	}

	recordAuthAudit(ctx, s.db, &user.ID, ActionAuthSetupAdmin, authClientIP(ctx), map[string]any{
		"username": username,
	})

	setCookie, err := s.issueSession(ctx, user, req.Header().Get("User-Agent"))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("issuing session: %w", err))
	}

	res := connect.NewResponse(&supervisorv1.SetupAdminResponse{
		Success: true,
	})
	res.Header().Set("Set-Cookie", setCookie)
	return res, nil
}

// Login verifies credentials behind the brute-force guard and issues a
// session on success (docs/32 §4). Both failure paths (unknown user, wrong
// password) run exactly one bcrypt comparison, share the coarse
// "invalid credentials" error and audit reason, and feed the same
// username+IP rate-limit key - response shape and timing cannot reveal
// whether an account exists.
func (s *AuthService) Login(ctx context.Context, req *connect.Request[supervisorv1.LoginRequest]) (*connect.Response[supervisorv1.LoginResponse], error) {
	username := strings.TrimSpace(req.Msg.Username)
	password := req.Msg.Password

	// Trim-aware username check: min_len sees the raw wire string, but lookups
	// use the trimmed value (password emptiness is class A).
	if username == "" {
		return nil, invalidArgument(newViolation(RuleAuthUsernameRequired, "username", "username must not be empty"))
	}

	clientIP := authClientIP(ctx)
	key := rateLimitKey(username, clientIP)

	if retry, locked := s.limiter.retryAfter(key); locked {
		recordAuthAudit(ctx, s.db, nil, ActionAuthRateLimited, clientIP, map[string]any{
			"username":    username,
			"retry_after": int(retry.Seconds()),
		})
		return nil, connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("too many failed login attempts, try again in %d seconds", int(retry.Seconds())))
	}

	user, lookupErr := s.db.GetAdminUserByUsername(ctx, username)
	if lookupErr != nil {
		// Unknown username: burn one bcrypt comparison against the startup
		// dummy hash and discard the result, then take the identical
		// failure path (docs/32 §4.3).
		_ = bcrypt.CompareHashAndPassword(s.dummyHashBytes(), []byte(password))
		s.loginFailure(ctx, nil, username, clientIP)
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid username or password"))
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		s.loginFailure(ctx, &user.ID, username, clientIP)
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid username or password"))
	}

	s.limiter.reset(key)

	setCookie, err := s.issueSession(ctx, user, req.Header().Get("User-Agent"))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("issuing session: %w", err))
	}

	recordAuthAudit(ctx, s.db, &user.ID, ActionAuthLoginSuccess, clientIP, map[string]any{
		"username": user.Username,
	})

	res := connect.NewResponse(&supervisorv1.LoginResponse{
		Success:  true,
		Username: user.Username,
	})
	res.Header().Set("Set-Cookie", setCookie)
	return res, nil
}

// loginFailure records one failed attempt in the rate limiter and the audit
// log with the coarse reason. userID stays nil for unknown usernames, which
// keeps the two paths indistinguishable in the response.
func (s *AuthService) loginFailure(ctx context.Context, userID *int64, username, clientIP string) {
	s.limiter.recordFailure(rateLimitKey(username, clientIP))
	recordAuthAudit(ctx, s.db, userID, ActionAuthLoginFailed, clientIP, map[string]any{
		"username": username,
		"reason":   "invalid_credentials",
	})
}

// GetSession reports the authenticated session (the interceptor has already
// validated the cookie and injected the user context) plus host facts.
func (s *AuthService) GetSession(ctx context.Context, req *connect.Request[supervisorv1.GetSessionRequest]) (*connect.Response[supervisorv1.GetSessionResponse], error) {
	user, ok := GetUserContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("unauthorized: missing session"))
	}

	return connect.NewResponse(&supervisorv1.GetSessionResponse{
		Username: user.Username,
		IsAdmin:  user.Role == "admin",
		HostArch: HostArch(),
		HostOs:   HostOS(),
	}), nil
}
