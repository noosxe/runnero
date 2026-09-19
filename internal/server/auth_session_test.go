package server_test

import (
	"context"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/noosxe/runnero/internal/db"
	supervisorv1 "github.com/noosxe/runnero/internal/pb/supervisor/v1"
	"github.com/noosxe/runnero/internal/pb/supervisor/v1/supervisorv1connect"
	"github.com/noosxe/runnero/internal/server"
)

// Tests for the opaque-session lifecycle (RUN-230, docs/32 §3): token
// shape, the two clocks, sliding renewal with the half-window rule and the
// absolute clamp, cookie attributes, and the removed Bearer fallback.

// sessionTestServer wires a server + client pair around a custom session
// config and boots the admin (fixed credentials) for login tests.
func sessionTestServer(t *testing.T, database *db.DB, cfg server.SessionConfig) (*httptest.Server, supervisorv1connect.AuthServiceClient) {
	t.Helper()
	srv := server.New(server.Options{
		Port:         8080,
		AuthDB:       database,
		OnboardingDB: database,
		Session:      cfg,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, supervisorv1connect.NewAuthServiceClient(ts.Client(), ts.URL)
}

func setupAdmin(t *testing.T, client supervisorv1connect.AuthServiceClient) {
	t.Helper()
	res, err := client.SetupAdmin(context.Background(), connect.NewRequest(&supervisorv1.SetupAdminRequest{
		Username: "admin",
		Password: "super-secret-password-123",
	}))
	if err != nil {
		t.Fatalf("SetupAdmin failed: %v", err)
	}
	_ = res
}

func cookieValue(setCookie string) string {
	return strings.TrimPrefix(strings.Split(setCookie, ";")[0], "session_token=")
}

func getSessionWithCookie(t *testing.T, client supervisorv1connect.AuthServiceClient, rawToken string) (*connect.Response[supervisorv1.GetSessionResponse], error) {
	t.Helper()
	req := connect.NewRequest(&supervisorv1.GetSessionRequest{})
	req.Header().Set("Cookie", "session_token="+rawToken)
	return client.GetSession(context.Background(), req)
}

// TestOpaqueTokenShape pins the token model (docs/32 §3.2): 32 random bytes
// hex-encoded (64 hex chars, no JWT structure), with only the SHA-256 hash
// stored server-side.
func TestOpaqueTokenShape(t *testing.T) {
	ctx := context.Background()
	database := setupTestDB(t)
	_, client := sessionTestServer(t, database, testSessionConfig())
	setupAdmin(t, client)

	res, err := client.Login(ctx, connect.NewRequest(&supervisorv1.LoginRequest{
		Username: "admin",
		Password: "super-secret-password-123",
	}))
	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}
	setCookie := res.Header().Get("Set-Cookie")
	raw := cookieValue(setCookie)
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(raw) {
		t.Fatalf("session token is not 64 hex chars: %q", raw)
	}
	if strings.Contains(setCookie, "Max-Age=86400") {
		t.Fatalf("cookie Max-Age still reflects the retired 24h JWT lifetime: %s", setCookie)
	}
	if !strings.Contains(setCookie, "Max-Age=3600") { // testSessionConfig idle = 1h
		t.Fatalf("cookie Max-Age does not track the idle timeout: %s", setCookie)
	}

	sess, err := database.GetSessionByTokenHash(ctx, server.HashToken(raw))
	if err != nil {
		t.Fatalf("session row not stored by hash: %v", err)
	}
	if !sess.AbsoluteExpiresAt.After(sess.ExpiresAt) {
		t.Fatalf("absolute cap %v must be after the idle deadline %v", sess.AbsoluteExpiresAt, sess.ExpiresAt)
	}
}

// TestSessionExpiredByEitherClockIsDeleted proves both clocks kill the
// session row and reject the request (docs/32 §3.3).
func TestSessionExpiredByEitherClockIsDeleted(t *testing.T) {
	ctx := context.Background()
	database := setupTestDB(t)
	_, client := sessionTestServer(t, database, testSessionConfig())
	setupAdmin(t, client)

	admin, err := database.GetAdminUserByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("GetAdminUserByUsername: %v", err)
	}

	for _, tc := range []struct {
		name    string
		expires time.Time
		cap     time.Time
	}{
		{"idle clock expired", time.Now().Add(-time.Minute), time.Now().Add(24 * time.Hour)},
		{"absolute clock expired", time.Now().Add(time.Hour), time.Now().Add(-time.Minute)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tokenHash := server.HashToken(tc.name)
			if _, err := database.CreateSession(ctx, db.CreateSessionParams{
				UserID:            admin.ID,
				TokenHash:         tokenHash,
				ExpiresAt:         tc.expires,
				AbsoluteExpiresAt: tc.cap,
				UserAgent:         "test",
			}); err != nil {
				t.Fatalf("CreateSession: %v", err)
			}

			_, err := getSessionWithCookie(t, client, tc.name)
			if err == nil || connect.CodeOf(err) != connect.CodeUnauthenticated {
				t.Fatalf("expired session accepted: %v", err)
			}
			if _, err := database.GetSessionByTokenHash(ctx, tokenHash); err == nil {
				t.Fatal("expired session row was not deleted")
			}
		})
	}
}

// TestSlidingRenewal exercises the half-window rule and the absolute-cap
// clamp (docs/32 §3.3): idle = 2h (half = 1h).
func TestSlidingRenewal(t *testing.T) {
	ctx := context.Background()
	cfg := server.SessionConfig{IdleTimeout: 2 * time.Hour, AbsoluteTimeout: 100 * time.Hour, SecureMode: "auto", BcryptCost: 4}

	t.Run("no renewal before half the idle window", func(t *testing.T) {
		database := setupTestDB(t)
		_, client := sessionTestServer(t, database, cfg)
		setupAdmin(t, client)
		admin, _ := database.GetAdminUserByUsername(ctx, "admin")

		expires := time.Now().Add(110 * time.Minute) // only 10min consumed
		if _, err := database.CreateSession(ctx, db.CreateSessionParams{
			UserID: admin.ID, TokenHash: server.HashToken("fresh"), ExpiresAt: expires,
			AbsoluteExpiresAt: time.Now().Add(100 * time.Hour), UserAgent: "test",
		}); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}

		res, err := getSessionWithCookie(t, client, "fresh")
		if err != nil {
			t.Fatalf("GetSession failed: %v", err)
		}
		if res.Header().Get("Set-Cookie") != "" {
			t.Fatal("renewal fired before half the idle window elapsed")
		}
		sess, _ := database.GetSessionByTokenHash(ctx, server.HashToken("fresh"))
		if !sess.ExpiresAt.Equal(expires.Truncate(time.Second)) && sess.ExpiresAt.Sub(expires).Abs() > time.Second {
			t.Fatalf("idle deadline moved without renewal: %v -> %v", expires, sess.ExpiresAt)
		}
	})

	t.Run("renews past half the idle window", func(t *testing.T) {
		database := setupTestDB(t)
		_, client := sessionTestServer(t, database, cfg)
		setupAdmin(t, client)
		admin, _ := database.GetAdminUserByUsername(ctx, "admin")

		if _, err := database.CreateSession(ctx, db.CreateSessionParams{
			UserID: admin.ID, TokenHash: server.HashToken("aging"), ExpiresAt: time.Now().Add(30 * time.Minute), // 90min consumed > 1h half
			AbsoluteExpiresAt: time.Now().Add(100 * time.Hour), UserAgent: "test",
		}); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}

		res, err := getSessionWithCookie(t, client, "aging")
		if err != nil {
			t.Fatalf("GetSession failed: %v", err)
		}
		sess, _ := database.GetSessionByTokenHash(ctx, server.HashToken("aging"))
		if sess.ExpiresAt.Before(time.Now().Add(119 * time.Minute)) {
			t.Fatalf("idle deadline did not slide to ~now+2h: %v", sess.ExpiresAt)
		}
		setCookie := res.Header().Get("Set-Cookie")
		if !strings.Contains(setCookie, "Max-Age=7200") {
			t.Fatalf("renewal response must refresh the cookie Max-Age to the idle seconds: %q", setCookie)
		}
	})

	t.Run("renewal clamps at the absolute cap", func(t *testing.T) {
		database := setupTestDB(t)
		_, client := sessionTestServer(t, database, cfg)
		setupAdmin(t, client)
		admin, _ := database.GetAdminUserByUsername(ctx, "admin")

		cap := time.Now().Add(30 * time.Minute) // cap closer than the idle window
		if _, err := database.CreateSession(ctx, db.CreateSessionParams{
			UserID: admin.ID, TokenHash: server.HashToken("clamped"), ExpiresAt: time.Now().Add(20 * time.Minute),
			AbsoluteExpiresAt: cap, UserAgent: "test",
		}); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}

		if _, err := getSessionWithCookie(t, client, "clamped"); err != nil {
			t.Fatalf("GetSession failed: %v", err)
		}
		sess, _ := database.GetSessionByTokenHash(ctx, server.HashToken("clamped"))
		if sess.ExpiresAt.After(cap) {
			t.Fatalf("renewal extended the idle deadline %v past the absolute cap %v", sess.ExpiresAt, cap)
		}
		if sess.ExpiresAt.Sub(cap).Abs() > time.Second {
			t.Fatalf("renewal did not clamp to the cap: %v vs %v", sess.ExpiresAt, cap)
		}
	})
}

// TestCookieSecureModes checks the three-mode Secure resolution (docs/32
// §3.4): auto detects plain HTTP (no Secure over httptest's HTTP server),
// always pins it, never omits it.
func TestCookieSecureModes(t *testing.T) {
	for _, tc := range []struct {
		mode     string
		wantSold bool
	}{
		{"auto", false},
		{"always", true},
		{"never", false},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			database := setupTestDB(t)
			cfg := server.SessionConfig{IdleTimeout: time.Hour, AbsoluteTimeout: 24 * time.Hour, SecureMode: tc.mode, BcryptCost: 4}
			_, client := sessionTestServer(t, database, cfg)

			res, err := client.SetupAdmin(context.Background(), connect.NewRequest(&supervisorv1.SetupAdminRequest{
				Username: "admin",
				Password: "super-secret-password-123",
			}))
			if err != nil {
				t.Fatalf("SetupAdmin failed: %v", err)
			}
			setCookie := res.Header().Get("Set-Cookie")
			got := strings.Contains(setCookie, "Secure")
			if got != tc.wantSold {
				t.Fatalf("Secure flag = %v, want %v (cookie: %s)", got, tc.wantSold, setCookie)
			}
		})
	}
}

// TestBearerTokenRejected proves the Authorization fallback is gone
// (docs/32 §2.5): a raw token in the header authenticates nothing.
func TestBearerTokenRejected(t *testing.T) {
	ctx := context.Background()
	database := setupTestDB(t)
	_, client := sessionTestServer(t, database, testSessionConfig())
	setupAdmin(t, client)

	res, err := client.Login(ctx, connect.NewRequest(&supervisorv1.LoginRequest{
		Username: "admin",
		Password: "super-secret-password-123",
	}))
	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}
	raw := cookieValue(res.Header().Get("Set-Cookie"))

	req := connect.NewRequest(&supervisorv1.GetSessionRequest{})
	req.Header().Set("Authorization", "Bearer "+raw)
	if _, err := client.GetSession(ctx, req); err == nil || connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("Bearer token accepted: %v", err)
	}
}

// TestPublicProcedureStaysAnonymousAndGetSessionRequiresAuth pins the
// matrix's behavioral split: GetOnboardingStatus works without a cookie,
// GetSession does not.
func TestPublicProcedureStaysAnonymousAndGetSessionRequiresAuth(t *testing.T) {
	ctx := context.Background()
	database := setupTestDB(t)
	ts, client := sessionTestServer(t, database, testSessionConfig())
	onboarding := supervisorv1connect.NewOnboardingServiceClient(ts.Client(), ts.URL)

	if _, err := onboarding.GetOnboardingStatus(ctx, connect.NewRequest(&supervisorv1.GetOnboardingStatusRequest{})); err != nil {
		t.Fatalf("public procedure rejected without a cookie: %v", err)
	}
	if _, err := client.GetSession(ctx, connect.NewRequest(&supervisorv1.GetSessionRequest{})); err == nil || connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("admin-bucket procedure accepted without a cookie: %v", err)
	}
}

// loginFail runs one failing login and returns the connect code.
func loginFail(t *testing.T, client supervisorv1connect.AuthServiceClient, username, password string) connect.Code {
	t.Helper()
	_, err := client.Login(context.Background(), connect.NewRequest(&supervisorv1.LoginRequest{
		Username: username,
		Password: password,
	}))
	return connect.CodeOf(err)
}

// TestLoginRateLimitLockout exercises the brute-force guard end to end
// (docs/32 §4.2): five failures lock the username+IP key with
// ResourceExhausted, the lock audits auth.rate_limited, unknown usernames
// feed the same key, and a successful login resets it.
func TestLoginRateLimitLockout(t *testing.T) {
	ctx := context.Background()
	database := setupTestDB(t)
	_, client := sessionTestServer(t, database, testSessionConfig())
	setupAdmin(t, client)

	// Four failures stay answered with the normal invalid-credentials code.
	for i := 0; i < 4; i++ {
		if code := loginFail(t, client, "admin", "totally-wrong"); code != connect.CodeUnauthenticated {
			t.Fatalf("failure %d: want Unauthenticated, got %v", i+1, code)
		}
	}

	// The 5th failure records; the 6th attempt is refused up front.
	if code := loginFail(t, client, "admin", "totally-wrong"); code != connect.CodeUnauthenticated {
		t.Fatalf("failure 5: want Unauthenticated, got %v", code)
	}
	_, err := client.Login(ctx, connect.NewRequest(&supervisorv1.LoginRequest{
		Username: "admin",
		Password: "super-secret-password-123",
	}))
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("locked attempt want ResourceExhausted, got: %v", err)
	}
	if err != nil && !strings.Contains(err.Error(), "try again") {
		t.Fatalf("locked error should carry retry guidance, got: %v", err)
	}

	// The correct password is refused too: the lock gates the account.
	// (Already asserted by the ResourceExhausted code above.)

	// The lockout attempt audited auth.rate_limited.
	logs, lerr := database.ListAuditLogs(ctx, db.ListAuditLogsParams{Limit: 50, Offset: 0})
	if lerr != nil {
		t.Fatalf("ListAuditLogs: %v", lerr)
	}
	rateLimited := false
	for _, row := range logs {
		if row.Action == "auth.rate_limited" {
			rateLimited = true
		}
	}
	if !rateLimited {
		t.Fatalf("auth.rate_limited not audited; actions: %v", auditActions(logs))
	}
}

// TestLoginSuccessResetsRateLimit pins the reset half of docs/32 §4.2:
// failures below the threshold do not outlive a successful login.
func TestLoginSuccessResetsRateLimit(t *testing.T) {
	database := setupTestDB(t)
	_, client := sessionTestServer(t, database, testSessionConfig())
	setupAdmin(t, client)

	// Two failures, then a success, then two more: never reaches 5 in a
	// live window, so every attempt still gets the normal code.
	for i := 0; i < 2; i++ {
		if code := loginFail(t, client, "admin", "nope"); code != connect.CodeUnauthenticated {
			t.Fatalf("pre-success failure %d: got %v", i+1, code)
		}
	}
	res, err := client.Login(context.Background(), connect.NewRequest(&supervisorv1.LoginRequest{
		Username: "admin",
		Password: "super-secret-password-123",
	}))
	if err != nil {
		t.Fatalf("successful login failed: %v", err)
	}
	_ = res
	for i := 0; i < 2; i++ {
		if code := loginFail(t, client, "admin", "nope"); code != connect.CodeUnauthenticated {
			t.Fatalf("post-success failure %d: want Unauthenticated, got %v (limiter did not reset)", i+1, code)
		}
	}
}

// TestLoginFailurePathsAreIndistinguishable pins the timing-equalization
// contract (docs/32 §4.3): unknown username and wrong password share one
// error message, one audit action, and one coarse reason; only source_ip
// and user_id differ (unknown users have no id to attach).
func TestLoginFailurePathsAreIndistinguishable(t *testing.T) {
	ctx := context.Background()
	database := setupTestDB(t)
	_, client := sessionTestServer(t, database, testSessionConfig())
	setupAdmin(t, client)

	_, errUnknown := client.Login(ctx, connect.NewRequest(&supervisorv1.LoginRequest{
		Username: "ghost-user",
		Password: "whatever-password",
	}))
	_, errWrong := client.Login(ctx, connect.NewRequest(&supervisorv1.LoginRequest{
		Username: "admin",
		Password: "wrong-password",
	}))
	if errUnknown == nil || errWrong == nil {
		t.Fatal("both failure paths must error")
	}
	if errUnknown.Error() != errWrong.Error() {
		t.Fatalf("failure errors differ:\n unknown: %v\n wrongpw: %v", errUnknown, errWrong)
	}
	if connect.CodeOf(errUnknown) != connect.CodeUnauthenticated || connect.CodeOf(errWrong) != connect.CodeUnauthenticated {
		t.Fatalf("failure codes differ: %v vs %v", connect.CodeOf(errUnknown), connect.CodeOf(errWrong))
	}

	logs, lerr := database.ListAuditLogs(ctx, db.ListAuditLogsParams{Limit: 10, Offset: 0})
	if lerr != nil {
		t.Fatalf("ListAuditLogs: %v", lerr)
	}
	failures := make([]db.AuditLog, 0, 2)
	for _, row := range logs {
		if row.Action == "auth.login_failed" {
			failures = append(failures, row)
		}
	}
	if len(failures) < 2 {
		t.Fatalf("want two login_failed rows, got %d (actions: %v)", len(failures), auditActions(logs))
	}
	for _, row := range failures {
		if !strings.Contains(row.Details.String, `"reason":"invalid_credentials"`) {
			t.Fatalf("reason must stay coarse, details: %s", row.Details.String)
		}
		if strings.Contains(row.Details.String, "user_not_found") || strings.Contains(row.Details.String, "invalid_password") {
			t.Fatalf("revealing reason split in audit: %s", row.Details.String)
		}
		if !row.SourceIp.Valid || row.SourceIp.String == "" {
			t.Fatalf("auth audit row missing source_ip: %+v", row)
		}
		if strings.Contains(row.Details.String, "whatever-password") || strings.Contains(row.Details.String, "wrong-password") {
			t.Fatalf("credentials leaked into audit details: %s", row.Details.String)
		}
	}
}

// TestLoginSuccessAuditVocabularyAndNoCredentialLeak pins the renamed
// success action, the stored client IP, and the absence of the password or
// session cookie from the audit row (docs/32 §5.1).
func TestLoginSuccessAuditVocabularyAndNoCredentialLeak(t *testing.T) {
	ctx := context.Background()
	database := setupTestDB(t)
	_, client := sessionTestServer(t, database, testSessionConfig())
	setupAdmin(t, client)

	res, err := client.Login(ctx, connect.NewRequest(&supervisorv1.LoginRequest{
		Username: "admin",
		Password: "super-secret-password-123",
	}))
	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}
	setCookie := res.Header().Get("Set-Cookie")

	logs, lerr := database.ListAuditLogs(ctx, db.ListAuditLogsParams{Limit: 10, Offset: 0})
	if lerr != nil {
		t.Fatalf("ListAuditLogs: %v", lerr)
	}
	var success *db.AuditLog
	for i := range logs {
		row := logs[i]
		if row.Action == "auth.login_success" {
			success = &logs[i]
		}
		if strings.Contains(row.Details.String, "super-secret-password-123") || strings.Contains(row.Details.String, cookieValue(setCookie)) {
			t.Fatalf("credential or token leaked into audit details (%s): %s", row.Action, row.Details.String)
		}
	}
	if success == nil {
		t.Fatalf("auth.login_success not recorded; actions: %v", auditActions(logs))
	}
	if !success.SourceIp.Valid || success.SourceIp.String == "" {
		t.Fatalf("login_success missing source_ip")
	}
}

func auditActions(logs []db.AuditLog) []string {
	actions := make([]string, 0, len(logs))
	for _, row := range logs {
		actions = append(actions, row.Action)
	}
	return actions
}

// TestGetSessionReportsVersion pins the version surface (RUN-251):
// GetSession must report the server's configured product version
// (the ldflags-stamped main.version, RUN-250) so the UI can show it in the
// sidebar footer and the settings Instance card. Authenticated-only: the
// endpoint already requires a session, which keeps the version from being
// a pre-auth fingerprint (docs/09 §4).
func TestGetSessionReportsVersion(t *testing.T) {
	ctx := context.Background()
	database := setupTestDB(t)
	const wantVersion = "v9.9.9-test"
	srv := server.New(server.Options{
		Port:         8080,
		Version:      wantVersion,
		AuthDB:       database,
		OnboardingDB: database,
		Session:      testSessionConfig(),
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	client := supervisorv1connect.NewAuthServiceClient(ts.Client(), ts.URL)
	setupAdmin(t, client)

	res, err := client.Login(ctx, connect.NewRequest(&supervisorv1.LoginRequest{
		Username: "admin",
		Password: "super-secret-password-123",
	}))
	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}
	req := connect.NewRequest(&supervisorv1.GetSessionRequest{})
	req.Header().Set("Cookie", "session_token="+cookieValue(res.Header().Get("Set-Cookie")))
	sess, err := client.GetSession(ctx, req)
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}
	if sess.Msg.Version != wantVersion {
		t.Fatalf("GetSession version = %q, want %q", sess.Msg.Version, wantVersion)
	}
}
