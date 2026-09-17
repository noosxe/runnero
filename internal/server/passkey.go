package server

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	supervisorv1 "github.com/noosxe/runnero/internal/pb/supervisor/v1"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/noosxe/runnero/internal/db"
)

// Passkey (WebAuthn) passwordless login and enrollment (RUN-247, docs/34).
//
// Two independent complete login paths (docs/34 §3.3): FinishPasskeyLogin
// identifies the user entirely via the discoverable credential and issues a
// standard session through the same issueSession path as the password
// Login; nothing chains between the paths.
//
// Ceremony state never leaves the server (docs/34 §3.4): go-webauthn's
// SessionData lives in an in-memory, single-use, TTL-bounded store keyed by
// the ceremony challenge. The client sees only the public options JSON; on
// Finish the browser-echoed challenge locates the ceremony.
//
// The anonymous surface is bounded (docs/34 §4.3): BeginPasskeyLogin runs
// with no credential at all, so the login pool caps concurrent ceremonies
// and every failure feeds the durable rate limiter.

// Passkey audit action vocabulary (docs/34 §8): auth.passkey.* exactly as
// docs/32 §5.1 reserved. Reasons stay coarse; details never carry
// credential IDs, challenge bytes, or attestation objects (the leakage
// tests assert this).
const (
	ActionAuthPasskeyEnrolled        = "auth.passkey.enrolled"
	ActionAuthPasskeyEnrollFailed    = "auth.passkey.enroll_failed"
	ActionAuthPasskeyRemoved         = "auth.passkey.removed"
	ActionAuthPasskeyRenamed         = "auth.passkey.renamed"
	ActionAuthPasskeyAssertionFailed = "auth.passkey.assertion_failed"
	ActionAuthPasskeyCloneWarning    = "auth.passkey.clone_warning"
)

// Ceremony bounds (docs/34 §3.4, §4.3).
const (
	passkeyCeremonyTTL   = 3 * time.Minute
	passkeyLoginPoolCap  = 32
	passkeyAnonLimitUser = "passkey-anon" // pseudo-user keying anonymous limiter entries
	DefaultPasskeyLabel  = "Passkey"
	// DefaultWebAuthnRPDisplayName is the static RP display name (docs/34
	// section 3.5).
	DefaultWebAuthnRPDisplayName = "Runnero"
	passkeyUserHandleBytes       = 8 // int64 admin user id, big-endian
)

// WebAuthnConfig carries the validated passkey configuration (docs/34
// §3.5) from cmd wiring into the server. A nil pointer or an empty RPID
// means the feature is completely off: every passkey RPC answers
// FailedPrecondition and Login behaves byte-for-byte as before.
type WebAuthnConfig struct {
	RPID          string
	RPDisplayName string
	Origins       []string
}

// Enabled reports whether passkey login is active. Empty config is
// fail-closed (docs/34 §3.5).
func (c *WebAuthnConfig) Enabled() bool {
	return c != nil && c.RPID != ""
}

// newWebAuthnEngine builds the go-webauthn entry point with passkey-grade
// registration options locked in config-level (docs/34 §3.7): discoverable
// credentials (resident key required — discoverability IS the login
// mechanism) and user verification required on every ceremony (UV stands
// where the password used to). Assertions re-assert UV via the login
// option at Begin time; the session carries it into validation.
func newWebAuthnEngine(cfg *WebAuthnConfig) (*webauthn.WebAuthn, error) {
	return webauthn.New(&webauthn.Config{
		RPID:          cfg.RPID,
		RPDisplayName: cfg.RPDisplayName,
		RPOrigins:     cfg.Origins,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			RequireResidentKey: protocol.ResidentKeyRequired(),
			UserVerification:   protocol.VerificationRequired,
		},
	})
}

// PasskeyStore is the database subset the passkey engine needs. *db.DB
// satisfies it directly via the sqlc-generated queries.
type PasskeyStore interface {
	CreateWebauthnCredential(ctx context.Context, arg db.CreateWebauthnCredentialParams) (db.WebauthnCredential, error)
	GetWebauthnCredentialById(ctx context.Context, credentialID []byte) (db.WebauthnCredential, error)
	GetWebauthnCredentialByIdAndUserId(ctx context.Context, arg db.GetWebauthnCredentialByIdAndUserIdParams) (db.WebauthnCredential, error)
	ListWebauthnCredentialsByUserId(ctx context.Context, userID int64) ([]db.WebauthnCredential, error)
	RenameWebauthnCredential(ctx context.Context, arg db.RenameWebauthnCredentialParams) (int64, error)
	DeleteWebauthnCredentialByIdAndUserId(ctx context.Context, arg db.DeleteWebauthnCredentialByIdAndUserIdParams) (int64, error)
	UpdateWebauthnCredentialAssertionState(ctx context.Context, arg db.UpdateWebauthnCredentialAssertionStateParams) error
	SetWebauthnCredentialCloneWarning(ctx context.Context, credentialID []byte) error
}

// ceremonyKind distinguishes the two ceremony pools sharing one store.
type ceremonyKind int

const (
	ceremonyLogin ceremonyKind = iota
	ceremonyEnroll
)

// passkeyCeremony is one live Begin→Finish pair. SessionData is consumed
// on first use (single-use, docs/34 §5.2) and expires after
// passkeyCeremonyTTL.
type passkeyCeremony struct {
	session   webauthn.SessionData
	kind      ceremonyKind
	userID    int64 // enrollment owner; unused for login ceremonies
	expiresAt time.Time
}

// passkeyCeremonyStore holds ceremony state in supervisor memory only
// (docs/34 §3.4). Login ceremonies share one bounded pool (cap
// passkeyLoginPoolCap — the anonymous surface grows state only up to this
// fixed bound, docs/34 §5.6); enrollment ceremonies are per-user with one
// live ceremony per user (a second Begin replaces the first). A supervisor
// restart drains the store, which is harmless: the client restarts the
// ceremony.
type passkeyCeremonyStore struct {
	mu    sync.Mutex
	now   func() time.Time
	items map[string]*passkeyCeremony // keyed by the ceremony challenge
}

func newPasskeyCeremonyStore() *passkeyCeremonyStore {
	return &passkeyCeremonyStore{
		now:   time.Now,
		items: make(map[string]*passkeyCeremony),
	}
}

// beginEnroll records a fresh enrollment ceremony for the user, replacing
// any prior one (single live ceremony per user, docs/34 §4.3).
func (s *passkeyCeremonyStore) beginEnroll(challenge string, userID int64, session webauthn.SessionData) {
	s.sweep()
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, c := range s.items {
		if c.kind == ceremonyEnroll && c.userID == userID {
			delete(s.items, k)
		}
	}
	s.items[challenge] = &passkeyCeremony{
		session:   session,
		kind:      ceremonyEnroll,
		userID:    userID,
		expiresAt: s.now().Add(passkeyCeremonyTTL),
	}
}

// beginLogin records a fresh anonymous login ceremony. When the pool is
// full the oldest-expired entry is evicted; a pool of live ceremonies
// rejects the Begin (fail-closed, no queue to fill — docs/34 §4.3).
func (s *passkeyCeremonyStore) beginLogin(challenge string, session webauthn.SessionData) bool {
	s.sweep()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.items) >= passkeyLoginPoolCap {
		var (
			oldestKey    string
			oldestExpiry time.Time
		)
		for k, c := range s.items {
			if c.kind != ceremonyLogin {
				continue
			}
			if oldestKey == "" || c.expiresAt.Before(oldestExpiry) {
				oldestKey, oldestExpiry = k, c.expiresAt
			}
		}
		if oldestKey == "" || s.now().Before(oldestExpiry) {
			return false
		}
		delete(s.items, oldestKey)
	}
	s.items[challenge] = &passkeyCeremony{
		session:   session,
		kind:      ceremonyLogin,
		expiresAt: s.now().Add(passkeyCeremonyTTL),
	}
	return true
}

// consume removes and returns the ceremony matching the challenge and
// kind. Single-use: a replayed challenge finds nothing. A userID of 0
// skips the enrollment-owner check (login ceremonies carry no user).
func (s *passkeyCeremonyStore) consume(challenge string, kind ceremonyKind, userID int64) (webauthn.SessionData, bool) {
	s.sweep()
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.items[challenge]
	if !ok || c.kind != kind {
		return webauthn.SessionData{}, false
	}
	if kind == ceremonyEnroll && userID != 0 && c.userID != userID {
		return webauthn.SessionData{}, false
	}
	if s.now().After(c.expiresAt) {
		delete(s.items, challenge)
		return webauthn.SessionData{}, false
	}
	delete(s.items, challenge)
	return c.session, true
}

// sweep drops expired ceremonies; called on every mutation so the pool
// self-cleans without a background ticker.
func (s *passkeyCeremonyStore) sweep() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for k, c := range s.items {
		if now.After(c.expiresAt) {
			delete(s.items, k)
		}
	}
}

// passkeyUser adapts an admin user row plus its stored credentials to the
// library's User interface. WebAuthnID is the stable big-endian user id —
// the authenticator echoes it back as the assertion's userHandle, and the
// discoverable validation path cross-checks the two (docs/34 §3.2).
type passkeyUser struct {
	user  db.AdminUser
	creds []webauthn.Credential
}

func (u *passkeyUser) WebAuthnID() []byte {
	id := u.user.ID
	out := make([]byte, passkeyUserHandleBytes)
	for i := 0; i < passkeyUserHandleBytes; i++ {
		out[passkeyUserHandleBytes-1-i] = byte(id >> (8 * i))
	}
	return out
}

func (u *passkeyUser) WebAuthnName() string                       { return u.user.Username }
func (u *passkeyUser) WebAuthnDisplayName() string                { return u.user.Username }
func (u *passkeyUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

// webauthnCredentialFromRow converts a stored row to the library's
// Credential for assertion validation.
func webauthnCredentialFromRow(row db.WebauthnCredential) webauthn.Credential {
	transports := parsePasskeyTransports(row.Transports)
	cred := webauthn.Credential{
		ID:              row.CredentialID,
		PublicKey:       row.PublicKey,
		AttestationType: row.AttestationType,
		Transport:       transports,
		Authenticator: webauthn.Authenticator{
			SignCount: uint32(row.SignCount),
		},
	}
	cred.Flags.BackupEligible = row.BackupEligible == 1
	cred.Flags.BackupState = row.BackupState == 1
	return cred
}

// parsePasskeyTransports decodes the CSV transport column into the
// library's transport constants, dropping unknown values.
func parsePasskeyTransports(csv string) []protocol.AuthenticatorTransport {
	if csv == "" {
		return nil
	}
	parts := strings.Split(csv, ",")
	out := make([]protocol.AuthenticatorTransport, 0, len(parts))
	for _, p := range parts {
		t := protocol.AuthenticatorTransport(strings.TrimSpace(p))
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// encodePasskeyTransports joins the library's transport constants back to
// the CSV column form.
func encodePasskeyTransports(ts []protocol.AuthenticatorTransport) string {
	if len(ts) == 0 {
		return ""
	}
	parts := make([]string, 0, len(ts))
	for _, t := range ts {
		parts = append(parts, string(t))
	}
	return strings.Join(parts, ",")
}

// passkeyInfo converts a stored row to its wire shape (docs/34 §7): ids and
// flags only, no credential material.
func passkeyInfo(row db.WebauthnCredential) *supervisorv1.PasskeyInfo {
	return &supervisorv1.PasskeyInfo{
		Id:             row.ID,
		Name:           row.Name,
		CreatedAt:      timestamppb.New(row.CreatedAt),
		LastUsedAt:     timestamppb.New(row.LastUsedAt),
		BackupEligible: row.BackupEligible == 1,
		BackupState:    row.BackupState == 1,
		CloneWarning:   row.CloneWarning == 1,
	}
}

// passkeyReady reports whether the passkey engine is active; every RPC
// short-circuits on it (fail-closed, docs/34 §3.5).
func (s *AuthService) passkeyReady() bool {
	return s.wa != nil && s.passkeys != nil && s.passkeyStore != nil
}

// BeginPasskeyEnrollment starts an enrollment ceremony after verifying the
// current password (docs/34 §4.2): a stolen-but-live session must not mint
// a new complete login identity. The session alone is proof of "was
// authenticated"; the password re-check restores "is authenticated" for an
// identity-granting action.
func (s *AuthService) BeginPasskeyEnrollment(ctx context.Context, req *connect.Request[supervisorv1.BeginPasskeyEnrollmentRequest]) (*connect.Response[supervisorv1.BeginPasskeyEnrollmentResponse], error) {
	if !s.passkeyReady() {
		return nil, passkeyUnavailable()
	}
	user, err := userContextFromAuth(ctx)
	if err != nil {
		return nil, err
	}

	row, err := s.db.GetAdminUserById(ctx, user.UserID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to load account"))
	}

	// One bcrypt comparison, same posture as ChangePassword (docs/32 §4.4).
	clientIP := authClientIP(ctx)
	if err := bcrypt.CompareHashAndPassword([]byte(row.PasswordHash), []byte(req.Msg.CurrentPassword)); err != nil {
		recordAuthAudit(ctx, s.db, &user.UserID, ActionAuthPasskeyEnrollFailed, clientIP, map[string]any{
			"username": user.Username,
			"reason":   "password_mismatch",
		})
		return nil, invalidArgument(newViolation(RuleAuthPasswordCurrentMismatch, "current_password", "current password is incorrect"))
	}

	// Close any prior ceremony for this user implicitly: a fresh Begin
	// replaces it in the store.
	pu := &passkeyUser{user: row}
	creation, session, err := s.wa.BeginRegistration(
		pu,
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			RequireResidentKey: protocol.ResidentKeyRequired(),
			UserVerification:   protocol.VerificationRequired,
		}),
	)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("starting enrollment ceremony: %w", err))
	}

	s.passkeys.beginEnroll(string(session.Challenge), user.UserID, *session)

	optsJSON, err := json.Marshal(creation)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("encoding enrollment options: %w", err))
	}
	return connect.NewResponse(&supervisorv1.BeginPasskeyEnrollmentResponse{
		PublicKeyOptionsJson: optsJSON,
	}), nil
}

// FinishPasskeyEnrollment validates the authenticator's attestation against
// the stored ceremony and persists the credential (public material only,
// docs/34 §3.6).
func (s *AuthService) FinishPasskeyEnrollment(ctx context.Context, req *connect.Request[supervisorv1.FinishPasskeyEnrollmentRequest]) (*connect.Response[supervisorv1.FinishPasskeyEnrollmentResponse], error) {
	if !s.passkeyReady() {
		return nil, passkeyUnavailable()
	}
	user, err := userContextFromAuth(ctx)
	if err != nil {
		return nil, err
	}
	clientIP := authClientIP(ctx)

	parsed, perr := protocol.ParseCredentialCreationResponseBody(strings.NewReader(string(req.Msg.AttestationResponseJson)))
	if perr != nil {
		s.recordPasskeyEnrollFailure(ctx, user, clientIP, "bad_attestation")
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid attestation response"))
	}

	session, ok := s.passkeys.consume(string(parsed.Response.CollectedClientData.Challenge), ceremonyEnroll, user.UserID)
	if !ok {
		s.recordPasskeyEnrollFailure(ctx, user, clientIP, "ceremony_expired")
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("enrollment ceremony expired or not found; start again"))
	}

	row, err := s.db.GetAdminUserById(ctx, user.UserID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to load account"))
	}

	credential, cerr := s.wa.CreateCredential(&passkeyUser{user: row}, session, parsed)
	if cerr != nil {
		s.recordPasskeyEnrollFailure(ctx, user, clientIP, "bad_attestation")
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("attestation verification failed"))
	}

	name := strings.TrimSpace(req.Msg.Name)
	if name == "" {
		name = DefaultPasskeyLabel
	}

	stored, serr := s.passkeyStore.CreateWebauthnCredential(ctx, db.CreateWebauthnCredentialParams{
		UserID:          user.UserID,
		Name:            name,
		CredentialID:    credential.ID,
		PublicKey:       credential.PublicKey,
		Aaguid:          hex.EncodeToString(credential.Authenticator.AAGUID),
		AttestationType: credential.AttestationType,
		Transports:      encodePasskeyTransports(credential.Transport),
		SignCount:       int64(credential.Authenticator.SignCount),
		BackupEligible:  boolToInt64(credential.Flags.BackupEligible),
		BackupState:     boolToInt64(credential.Flags.BackupState),
		CloneWarning:    0,
	})
	if serr != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("storing credential: %w", serr))
	}

	recordAuthAudit(ctx, s.db, &user.UserID, ActionAuthPasskeyEnrolled, clientIP, map[string]any{
		"username":    user.Username,
		"label":       name,
		"transports":  encodePasskeyTransports(credential.Transport),
		"backup_flag": credential.Flags.BackupEligible,
	})

	return connect.NewResponse(&supervisorv1.FinishPasskeyEnrollmentResponse{
		Passkey: passkeyInfo(stored),
	}), nil
}

// recordPasskeyEnrollFailure writes the coarse audit row for a failed
// enrollment Finish (docs/34 §8).
func (s *AuthService) recordPasskeyEnrollFailure(ctx context.Context, user *UserContext, clientIP, reason string) {
	recordAuthAudit(ctx, s.db, &user.UserID, ActionAuthPasskeyEnrollFailed, clientIP, map[string]any{
		"username": user.Username,
		"reason":   reason,
	})
}

// ListPasskeys returns the caller's credentials as wire metadata only.
func (s *AuthService) ListPasskeys(ctx context.Context, _ *connect.Request[supervisorv1.ListPasskeysRequest]) (*connect.Response[supervisorv1.ListPasskeysResponse], error) {
	if !s.passkeyReady() {
		return nil, passkeyUnavailable()
	}
	user, err := userContextFromAuth(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.passkeyStore.ListWebauthnCredentialsByUserId(ctx, user.UserID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("listing credentials: %w", err))
	}
	out := make([]*supervisorv1.PasskeyInfo, 0, len(rows))
	for _, row := range rows {
		out = append(out, passkeyInfo(row))
	}
	return connect.NewResponse(&supervisorv1.ListPasskeysResponse{Passkeys: out}), nil
}

// RenamePasskey relabels one of the caller's credentials; foreign or
// unknown ids answer NotFound via the execrows ownership check.
func (s *AuthService) RenamePasskey(ctx context.Context, req *connect.Request[supervisorv1.RenamePasskeyRequest]) (*connect.Response[supervisorv1.RenamePasskeyResponse], error) {
	if !s.passkeyReady() {
		return nil, passkeyUnavailable()
	}
	user, err := userContextFromAuth(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.Msg.Name)
	if name == "" {
		name = DefaultPasskeyLabel
	}
	updated, err := s.passkeyStore.RenameWebauthnCredential(ctx, db.RenameWebauthnCredentialParams{
		Name:   name,
		ID:     req.Msg.Id,
		UserID: user.UserID,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("renaming credential: %w", err))
	}
	if updated == 0 {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("passkey not found"))
	}
	recordAuthAudit(ctx, s.db, &user.UserID, ActionAuthPasskeyRenamed, authClientIP(ctx), map[string]any{
		"username": user.Username,
	})
	return connect.NewResponse(&supervisorv1.RenamePasskeyResponse{}), nil
}

// DeletePasskey removes one of the caller's credentials. Sessions issued
// earlier through it stay valid on purpose (docs/34 §4.4) — session
// revocation is the lever for that, and the UI copy says so.
func (s *AuthService) DeletePasskey(ctx context.Context, req *connect.Request[supervisorv1.DeletePasskeyRequest]) (*connect.Response[supervisorv1.DeletePasskeyResponse], error) {
	if !s.passkeyReady() {
		return nil, passkeyUnavailable()
	}
	user, err := userContextFromAuth(ctx)
	if err != nil {
		return nil, err
	}
	deleted, err := s.passkeyStore.DeleteWebauthnCredentialByIdAndUserId(ctx, db.DeleteWebauthnCredentialByIdAndUserIdParams{
		ID:     req.Msg.Id,
		UserID: user.UserID,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("deleting credential: %w", err))
	}
	if deleted == 0 {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("passkey not found"))
	}
	recordAuthAudit(ctx, s.db, &user.UserID, ActionAuthPasskeyRemoved, authClientIP(ctx), map[string]any{
		"username": user.Username,
	})
	return connect.NewResponse(&supervisorv1.DeletePasskeyResponse{}), nil
}

// BeginPasskeyLogin starts the passwordless discoverable ceremony. It runs
// with no credential at all (docs/34 §4.3): the ceremony pool bounds the
// anonymous state, the durable limiter (keyed by a fixed pseudo-user +
// client IP) refuses Begin while the key is locked out, and a full pool
// answers ResourceExhausted instead of queueing.
func (s *AuthService) BeginPasskeyLogin(ctx context.Context, _ *connect.Request[supervisorv1.BeginPasskeyLoginRequest]) (*connect.Response[supervisorv1.BeginPasskeyLoginResponse], error) {
	if !s.passkeyReady() {
		return nil, passkeyUnavailable()
	}

	clientIP := authClientIP(ctx)
	key := rateLimitKey(passkeyAnonLimitUser, clientIP)
	if retry, locked := s.limiter.retryAfter(key); locked {
		recordAuthAudit(ctx, s.db, nil, ActionAuthRateLimited, clientIP, map[string]any{
			"username":    passkeyAnonLimitUser,
			"method":      "passkey",
			"retry_after": int(retry.Seconds()),
		})
		return nil, connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("too many failed login attempts, try again in %d seconds", int(retry.Seconds())))
	}

	assertion, session, err := s.wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("starting login ceremony: %w", err))
	}
	if !s.passkeys.beginLogin(string(session.Challenge), *session) {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("too many pending passkey ceremonies; try again shortly"))
	}

	optsJSON, err := json.Marshal(assertion)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("encoding assertion options: %w", err))
	}
	return connect.NewResponse(&supervisorv1.BeginPasskeyLoginResponse{
		PublicKeyOptionsJson: optsJSON,
	}), nil
}

// FinishPasskeyLogin completes the passwordless login: resolve the user
// from the asserted credential ID (the credential-ID lookup IS the identity
// mechanism, docs/34 §3.2), enforce the counter policy, and issue a
// standard session through the shared issuance path. Failures feed the
// durable limiter keyed by client IP, upgrading to the username+IP key
// once the assertion identifies the user (docs/34 §4.3).
func (s *AuthService) FinishPasskeyLogin(ctx context.Context, req *connect.Request[supervisorv1.FinishPasskeyLoginRequest]) (*connect.Response[supervisorv1.LoginResponse], error) {
	if !s.passkeyReady() {
		return nil, passkeyUnavailable()
	}
	clientIP := authClientIP(ctx)

	parsed, perr := protocol.ParseCredentialRequestResponseBody(strings.NewReader(string(req.Msg.AssertionResponseJson)))
	if perr != nil {
		s.recordPasskeyAssertionFailure(ctx, nil, clientIP, "ceremony_invalid")
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid assertion response"))
	}

	// UV stands where the password used to (docs/34 §5.4): reject
	// UV-less assertions up front with the coarse reason, before any
	// signature work.
	if !parsed.Response.AuthenticatorData.Flags.HasUserVerified() {
		s.recordPasskeyAssertionFailure(ctx, nil, clientIP, "uv_missing")
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("passkey assertion rejected"))
	}

	session, ok := s.passkeys.consume(string(parsed.Response.CollectedClientData.Challenge), ceremonyLogin, 0)
	if !ok {
		s.recordPasskeyAssertionFailure(ctx, nil, clientIP, "ceremony_invalid")
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("passkey assertion rejected"))
	}

	// Identity lookup: raw credential id → row → user. Unknown or
	// already-flagged credentials fail exactly like bad signatures.
	var identified *UserContext
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		credRow, err := s.passkeyStore.GetWebauthnCredentialById(ctx, rawID)
		if err != nil {
			return nil, errUnknownPasskeyCredential
		}
		if credRow.CloneWarning == 1 {
			return nil, errPasskeyFlagged
		}
		userRow, err := s.db.GetAdminUserById(ctx, credRow.UserID)
		if err != nil {
			return nil, errUnknownPasskeyCredential
		}
		identified = &UserContext{
			UserID:   userRow.ID,
			Username: userRow.Username,
			Role:     userRow.Role,
		}
		// Assert the echoed userHandle matches the resolved user (the
		// library re-checks this against the adapter's WebAuthnID).
		creds, err := s.passkeyStore.ListWebauthnCredentialsByUserId(ctx, userRow.ID)
		if err != nil {
			return nil, errUnknownPasskeyCredential
		}
		waCreds := make([]webauthn.Credential, 0, len(creds))
		for _, c := range creds {
			waCreds = append(waCreds, webauthnCredentialFromRow(c))
		}
		pu := &passkeyUser{user: userRow, creds: waCreds}
		if len(userHandle) > 0 && !equalBytes(userHandle, pu.WebAuthnID()) {
			return nil, errUnknownPasskeyCredential
		}
		return pu, nil
	}

	user, credential, verr := s.wa.ValidatePasskeyLogin(handler, session, parsed)
	if verr != nil || user == nil || credential == nil {
		s.recordPasskeyAssertionFailure(ctx, identified, clientIP, "bad_signature")
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("passkey assertion rejected"))
	}

	// Counter policy (docs/34 §3.8): the library flags a non-increasing
	// non-zero counter on the returned credential (0<->0 counterless
	// authenticators are exempt). Fail closed: flag the row, audit, reject.
	if credential.Authenticator.CloneWarning {
		_ = s.passkeyStore.SetWebauthnCredentialCloneWarning(ctx, credential.ID)
		recordAuthAudit(ctx, s.db, identifiedUserID(identified), ActionAuthPasskeyCloneWarning, clientIP, map[string]any{
			"username": identifiedUsername(identified),
		})
		s.recordPasskeyAssertionFailure(ctx, identified, clientIP, "counter_regression")
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("passkey assertion rejected"))
	}

	// Persist the counter/flag result of the assertion.
	_ = s.passkeyStore.UpdateWebauthnCredentialAssertionState(ctx, db.UpdateWebauthnCredentialAssertionStateParams{
		SignCount:      int64(credential.Authenticator.SignCount),
		BackupEligible: boolToInt64(credential.Flags.BackupEligible),
		BackupState:    boolToInt64(credential.Flags.BackupState),
		LastUsedAt:     time.Now(),
		CredentialID:   credential.ID,
	})

	// Success clears both limiter keys (anonymous and identified).
	s.limiter.reset(rateLimitKey(passkeyAnonLimitUser, clientIP))
	s.limiter.reset(rateLimitKey(identified.Username, clientIP))

	row := user.(*passkeyUser).user
	setCookie, err := s.issueSession(ctx, row, "")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("issuing session: %w", err))
	}

	recordAuthAudit(ctx, s.db, &row.ID, ActionAuthLoginSuccess, clientIP, map[string]any{
		"username": row.Username,
		"method":   "passkey",
	})

	res := connect.NewResponse(&supervisorv1.LoginResponse{
		Success:  true,
		Username: row.Username,
	})
	res.Header().Set("Set-Cookie", setCookie)
	return res, nil
}

// recordPasskeyAssertionFailure writes the coarse audit row and feeds the
// durable limiter: anonymous failures key on the pseudo-user, identified
// ones upgrade to the standard username+IP key (docs/34 §4.3).
func (s *AuthService) recordPasskeyAssertionFailure(ctx context.Context, identified *UserContext, clientIP, reason string) {
	if identified != nil {
		s.limiter.recordFailure(rateLimitKey(identified.Username, clientIP))
	} else {
		s.limiter.recordFailure(rateLimitKey(passkeyAnonLimitUser, clientIP))
	}
	var uid *int64
	if identified != nil {
		uid = &identified.UserID
	}
	recordAuthAudit(ctx, s.db, uid, ActionAuthPasskeyAssertionFailed, clientIP, map[string]any{
		"username": identifiedUsername(identified),
		"reason":   reason,
	})
}

// Sentinel errors for the discoverable handler: coarse by design — the
// caller cannot distinguish "unknown credential" from "flagged credential"
// in the wire response.
var (
	errUnknownPasskeyCredential = errors.New("unknown passkey credential")
	errPasskeyFlagged           = errors.New("passkey credential is flagged")
)

func identifiedUsername(u *UserContext) string {
	if u == nil {
		return ""
	}
	return u.Username
}

func identifiedUserID(u *UserContext) *int64 {
	if u == nil {
		return nil
	}
	return &u.UserID
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func boolToInt64(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// passkeyUnavailable is the fail-closed answer of every passkey RPC when
// webauthn_rp_id is unset (docs/34 §3.5).
func passkeyUnavailable() error {
	return connect.NewError(connect.CodeFailedPrecondition, errors.New("passkey login is not configured on this deployment"))
}
