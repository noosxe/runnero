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
