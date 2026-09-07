package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/golang-jwt/jwt/v5"
	"github.com/noosxe/runnero/internal/db"
	supervisorv1 "github.com/noosxe/runnero/internal/pb/supervisor/v1"
	"github.com/noosxe/runnero/internal/pb/supervisor/v1/supervisorv1connect"
	"golang.org/x/crypto/bcrypt"
)

const (
	// SessionCookieName is the HttpOnly cookie carrying the admin JWT session token (docs/05 §5, OQ #6).
	SessionCookieName = "session_token"

	// SessionDuration is the 24-hour lifetime of admin session tokens (OQ #6).
	SessionDuration = 24 * time.Hour
)

// AuthDatabase defines the database subset required by the authentication engine and interceptor.
// *db.DB satisfies this interface directly.
type AuthDatabase interface {
	CountAdminUsers(ctx context.Context) (int64, error)
	CreateAdminUser(ctx context.Context, arg db.CreateAdminUserParams) (db.AdminUser, error)
	GetAdminUserByUsername(ctx context.Context, username string) (db.AdminUser, error)
	CreateSession(ctx context.Context, arg db.CreateSessionParams) (db.Session, error)
	GetSessionByTokenHash(ctx context.Context, tokenHash string) (db.Session, error)
	DeleteSessionByTokenHash(ctx context.Context, tokenHash string) error
	CreateAuditLog(ctx context.Context, arg db.CreateAuditLogParams) (db.AuditLog, error)
}

// Claims represents the JWT claims payload for administrator sessions.
type Claims struct {
	UserID   int64  `json:"uid"`
	Username string `json:"name"`
	jwt.RegisteredClaims
}

type contextKey string

const userContextKey contextKey = "supervisor.auth.user"

// UserContext carries authenticated administrator details across the request lifecycle.
type UserContext struct {
	UserID   int64
	Username string
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

// HashToken computes the deterministic SHA-256 hex string of a raw token string (OQ #11).
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// GenerateToken creates and signs a new JWT session token for the user.
func GenerateToken(userID int64, username string, secret []byte, duration time.Duration) (string, time.Time, error) {
	if len(secret) == 0 {
		return "", time.Time{}, errors.New("jwt secret must not be empty")
	}
	expiresAt := time.Now().Add(duration)
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", time.Time{}, fmt.Errorf("generating token nonce: %w", err)
	}
	claims := Claims{
		UserID:   userID,
		Username: username,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        hex.EncodeToString(nonce[:]),
			Subject:   strconv.FormatInt(userID, 10),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("signing token: %w", err)
	}
	return signed, expiresAt, nil
}

// ValidateToken parses and cryptographically validates a JWT token string against the secret.
func ValidateToken(tokenString string, secret []byte) (*Claims, error) {
	if len(secret) == 0 {
		return nil, errors.New("jwt secret must not be empty")
	}
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return secret, nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, errors.New("invalid token claims")
	}
	return claims, nil
}

// ExtractSessionToken extracts the session token from cookies or Authorization Bearer header.
func ExtractSessionToken(header http.Header) string {
	r := &http.Request{Header: header}
	if cookie, err := r.Cookie(SessionCookieName); err == nil && cookie.Value != "" {
		return cookie.Value
	}
	auth := header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	return ""
}

// IsPublicProcedure determines whether a Connect procedure can be accessed without authentication.
func IsPublicProcedure(procedure string) bool {
	switch procedure {
	case "/supervisor.v1.AuthService/SetupAdmin",
		"/supervisor.v1.AuthService/Login",
		"/supervisor.v1.OnboardingService/GetOnboardingStatus":
		return true
	default:
		return false
	}
}

// AuthInterceptor enforces valid cookie session authentication on protected procedures (unary and streaming).
type AuthInterceptor struct {
	authDB    AuthDatabase
	jwtSecret []byte
}

// NewAuthInterceptor returns a Connect Interceptor enforcing valid cookie session authentication
// on all protected procedures (both unary and streaming).
func NewAuthInterceptor(authDB AuthDatabase, jwtSecret []byte) connect.Interceptor {
	return &AuthInterceptor{
		authDB:    authDB,
		jwtSecret: jwtSecret,
	}
}

func (a *AuthInterceptor) authenticate(ctx context.Context, header http.Header, procedure string) (context.Context, error) {
	if IsPublicProcedure(procedure) {
		return ctx, nil
	}

	tokenString := ExtractSessionToken(header)
	if tokenString == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("unauthorized: missing session token"))
	}

	claims, err := ValidateToken(tokenString, a.jwtSecret)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("unauthorized: %w", err))
	}

	tokenHash := HashToken(tokenString)
	sess, err := a.authDB.GetSessionByTokenHash(ctx, tokenHash)
	if err != nil || sess.ExpiresAt.Before(time.Now()) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("unauthorized: session revoked or expired"))
	}

	ctx = WithUserContext(ctx, &UserContext{
		UserID:   claims.UserID,
		Username: claims.Username,
	})
	return ctx, nil
}

func (a *AuthInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		authCtx, err := a.authenticate(ctx, req.Header(), req.Spec().Procedure)
		if err != nil {
			return nil, err
		}
		return next(authCtx, req)
	}
}

func (a *AuthInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (a *AuthInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		authCtx, err := a.authenticate(ctx, conn.RequestHeader(), conn.Spec().Procedure)
		if err != nil {
			return err
		}
		return next(authCtx, conn)
	}
}

// AuthService implements supervisorv1connect.AuthServiceHandler.
type AuthService struct {
	supervisorv1connect.UnimplementedAuthServiceHandler
	db             AuthDatabase
	jwtSecret      []byte
	isSecureCookie bool
}

// NewAuthService constructs an AuthService instance.
func NewAuthService(db AuthDatabase, jwtSecret []byte, isSecureCookie bool) *AuthService {
	return &AuthService{
		db:             db,
		jwtSecret:      jwtSecret,
		isSecureCookie: isSecureCookie,
	}
}

// SetupAdmin creates the initial local administrator account. Fails if an administrator already exists.
func (s *AuthService) SetupAdmin(ctx context.Context, req *connect.Request[supervisorv1.SetupAdminRequest]) (*connect.Response[supervisorv1.SetupAdminResponse], error) {
	username := strings.TrimSpace(req.Msg.Username)
	password := req.Msg.Password

	if username == "" || password == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("username and password must not be empty"))
	}

	count, err := s.db.CountAdminUsers(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("checking existing admin users: %w", err))
	}
	if count > 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("administrator already configured"))
	}

	hashBytes, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
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

	recordAuditLogWithUser(ctx, s.db, &user.ID, "auth.setup_admin", "admin_user", &user.ID, map[string]any{
		"username": username,
	})

	tokenString, expiresAt, err := GenerateToken(user.ID, user.Username, s.jwtSecret, SessionDuration)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("generating session token: %w", err))
	}

	tokenHash := HashToken(tokenString)
	_, err = s.db.CreateSession(ctx, db.CreateSessionParams{
		UserID:    user.ID,
		TokenHash: tokenHash,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("recording session: %w", err))
	}

	cookie := &http.Cookie{
		Name:     SessionCookieName,
		Value:    tokenString,
		Path:     "/",
		MaxAge:   int(SessionDuration.Seconds()),
		HttpOnly: true,
		Secure:   s.isSecureCookie,
		SameSite: http.SameSiteStrictMode,
	}

	res := connect.NewResponse(&supervisorv1.SetupAdminResponse{
		Success: true,
	})
	res.Header().Set("Set-Cookie", cookie.String())
	return res, nil
}

// Login verifies credentials, creates an active session in SQLite, and returns an HttpOnly session cookie.
func (s *AuthService) Login(ctx context.Context, req *connect.Request[supervisorv1.LoginRequest]) (*connect.Response[supervisorv1.LoginResponse], error) {
	username := strings.TrimSpace(req.Msg.Username)
	password := req.Msg.Password

	if username == "" || password == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("username and password must not be empty"))
	}

	user, err := s.db.GetAdminUserByUsername(ctx, username)
	if err != nil {
		// Log failed attempt to audit_logs
		recordAuditLogWithUser(ctx, s.db, nil, "auth.login_failed", "admin_user", nil, map[string]any{
			"username": username,
			"reason":   "user_not_found",
		})
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid username or password"))
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		recordAuditLogWithUser(ctx, s.db, &user.ID, "auth.login_failed", "admin_user", &user.ID, map[string]any{
			"username": username,
			"reason":   "invalid_password",
		})
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid username or password"))
	}

	tokenString, expiresAt, err := GenerateToken(user.ID, user.Username, s.jwtSecret, SessionDuration)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("generating session token: %w", err))
	}

	tokenHash := HashToken(tokenString)
	_, err = s.db.CreateSession(ctx, db.CreateSessionParams{
		UserID:    user.ID,
		TokenHash: tokenHash,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("recording session: %w", err))
	}

	recordAuditLogWithUser(ctx, s.db, &user.ID, "auth.login", "admin_user", &user.ID, map[string]any{
		"username": username,
		"status":   "success",
	})

	cookie := &http.Cookie{
		Name:     SessionCookieName,
		Value:    tokenString,
		Path:     "/",
		MaxAge:   int(SessionDuration.Seconds()),
		HttpOnly: true,
		Secure:   s.isSecureCookie,
		SameSite: http.SameSiteStrictMode,
	}

	res := connect.NewResponse(&supervisorv1.LoginResponse{
		Success:  true,
		Username: user.Username,
	})
	res.Header().Set("Set-Cookie", cookie.String())
	return res, nil
}

// GetSession validates the current session from cookie and returns the active username.
func (s *AuthService) GetSession(ctx context.Context, req *connect.Request[supervisorv1.GetSessionRequest]) (*connect.Response[supervisorv1.GetSessionResponse], error) {
	tokenString := ExtractSessionToken(req.Header())
	if tokenString == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("unauthorized: missing session token"))
	}

	claims, err := ValidateToken(tokenString, s.jwtSecret)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("unauthorized: %w", err))
	}

	tokenHash := HashToken(tokenString)
	sess, err := s.db.GetSessionByTokenHash(ctx, tokenHash)
	if err != nil || sess.ExpiresAt.Before(time.Now()) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("unauthorized: session revoked or expired"))
	}

	return connect.NewResponse(&supervisorv1.GetSessionResponse{
		Username: claims.Username,
		IsAdmin:  true,
		HostArch: HostArch(),
		HostOs:   HostOS(),
	}), nil
}
