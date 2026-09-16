package server_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/noosxe/runnero/internal/db"
	supervisorv1 "github.com/noosxe/runnero/internal/pb/supervisor/v1"
	"github.com/noosxe/runnero/internal/pb/supervisor/v1/supervisorv1connect"
	"github.com/noosxe/runnero/internal/server"
)

// Tests for the session control surface (RUN-232, docs/32 §3.5): Logout,
// ListSessions, RevokeSession and RevokeOtherSessions - server-side
// deletion, caller-scoped rows, the current-session marker, and the audit
// vocabulary.

// loginWithUA logs in with an explicit User-Agent so ListSessions device
// labels are deterministic, and returns the raw opaque token.
func loginWithUA(t *testing.T, client supervisorv1connect.AuthServiceClient, userAgent string) string {
	t.Helper()
	req := connect.NewRequest(&supervisorv1.LoginRequest{Username: "admin", Password: "super-secret-password-123"})
	if userAgent != "" {
		req.Header().Set("User-Agent", userAgent)
	}
	res, err := client.Login(context.Background(), req)
	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}
	return cookieValue(res.Header().Get("Set-Cookie"))
}

// cookiedRequest wraps msg in a connect request carrying the session cookie.
func cookiedRequest[T any](t *testing.T, msg *T, token string) *connect.Request[T] {
	t.Helper()
	req := connect.NewRequest(msg)
	req.Header().Set("Cookie", "session_token="+token)
	return req
}

// requireExpiringCookie fails unless setCookie expires the session cookie.
func requireExpiringCookie(t *testing.T, setCookie string) {
	t.Helper()
	if setCookie == "" {
		t.Fatal("expected an expiring Set-Cookie header, got none")
	}
	if !strings.Contains(setCookie, "Max-Age=0") {
		t.Fatalf("Set-Cookie does not expire the cookie: %q", setCookie)
	}
}

func TestLogoutDeletesSessionAndClearsCookie(t *testing.T) {
	ctx := context.Background()
	database := setupTestDB(t)
	_, client := sessionTestServer(t, database, testSessionConfig())
	setupAdmin(t, client)

	token := loginWithUA(t, client, "Mozilla/5.0 (X11; Linux x86_64; rv:130.0) Gecko/20100101 Firefox/130.0")

	res, err := client.Logout(ctx, cookiedRequest(t, &supervisorv1.LogoutRequest{}, token))
	if err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if !res.Msg.Success {
		t.Fatal("Logout returned success=false")
	}
	requireExpiringCookie(t, res.Header().Get("Set-Cookie"))

	// The old token must be dead server-side (docs/32 §1 G4).
	if _, err := getSessionWithCookie(t, client, token); err == nil || connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("session survived logout: %v", err)
	}
	if _, err := database.GetSessionByTokenHash(ctx, server.HashToken(token)); err == nil {
		t.Fatal("logout session row still present")
	}

	logs, err := database.ListAuditLogs(ctx, db.ListAuditLogsParams{Limit: 50, Offset: 0})
	if err != nil {
		t.Fatalf("ListAuditLogs: %v", err)
	}
	var logoutRows []db.AuditLog
	for i := range logs {
		if logs[i].Action == "auth.logout" {
			logoutRows = append(logoutRows, logs[i])
		}
	}
	if len(logoutRows) != 1 {
		t.Fatalf("want exactly one auth.logout audit row, got %d (actions: %v)", len(logoutRows), auditActions(logs))
	}
	if !logoutRows[0].SourceIp.Valid || logoutRows[0].SourceIp.String == "" {
		t.Fatal("auth.logout audit row missing source_ip")
	}
}

func TestListSessionsMarksCurrentAndLabelsDevices(t *testing.T) {
	ctx := context.Background()
	database := setupTestDB(t)
	_, client := sessionTestServer(t, database, testSessionConfig())
	setupAdmin(t, client)

	firefox := loginWithUA(t, client, "Mozilla/5.0 (X11; Linux x86_64; rv:130.0) Gecko/20100101 Firefox/130.0")
	curl := loginWithUA(t, client, "curl/8.5.0")

	res, err := client.ListSessions(ctx, cookiedRequest(t, &supervisorv1.ListSessionsRequest{}, curl))
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	// SetupAdmin issues its own session, so two logins make three rows.
	if len(res.Msg.Sessions) != 3 {
		t.Fatalf("want 3 sessions, got %d", len(res.Msg.Sessions))
	}

	current := 0
	for _, s := range res.Msg.Sessions {
		if s.IsCurrent {
			current++
			if s.DeviceLabel != "curl 8.5.0" {
				t.Fatalf("current session label = %q, want curl 8.5.0", s.DeviceLabel)
			}
		}
	}
	if current != 1 {
		t.Fatalf("want exactly one current session marker, got %d", current)
	}

	labels := map[string]bool{}
	for _, s := range res.Msg.Sessions {
		labels[s.DeviceLabel] = true
	}
	if !labels["Firefox 130 on Linux"] {
		t.Fatalf("firefox session label missing: %v", labels)
	}

	// Listing from the other session must move the current marker.
	res2, err := client.ListSessions(ctx, cookiedRequest(t, &supervisorv1.ListSessionsRequest{}, firefox))
	if err != nil {
		t.Fatalf("ListSessions (firefox): %v", err)
	}
	for _, s := range res2.Msg.Sessions {
		if s.IsCurrent && s.DeviceLabel != "Firefox 130 on Linux" {
			t.Fatalf("current marker on wrong row: %+v", s)
		}
	}
}

func TestRevokeSessionOwnershipGuardAndCurrentLogout(t *testing.T) {
	ctx := context.Background()
	database := setupTestDB(t)
	_, client := sessionTestServer(t, database, testSessionConfig())
	setupAdmin(t, client)

	// A session owned by a different user: the SQL ownership guard must
	// make revoking it indistinguishable from an unknown id (docs/32 §3.5).
	stranger, err := database.CreateAdminUser(ctx, db.CreateAdminUserParams{
		Username:     "stranger",
		PasswordHash: "not-a-login",
	})
	if err != nil {
		t.Fatalf("CreateAdminUser: %v", err)
	}
	foreign, err := database.CreateSession(ctx, db.CreateSessionParams{
		UserID:            stranger.ID,
		TokenHash:         server.HashToken("foreign-token"),
		ExpiresAt:         time.Now().Add(time.Hour),
		AbsoluteExpiresAt: time.Now().Add(24 * time.Hour),
		UserAgent:         "foreign",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	mine := loginWithUA(t, client, "curl/8.5.0")

	// Foreign id: NotFound, and the row survives.
	_, err = client.RevokeSession(ctx, cookiedRequest(t, &supervisorv1.RevokeSessionRequest{SessionId: foreign.ID}, mine))
	if err == nil || connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("foreign session revoke = %v, want NotFound", err)
	}
	if rows, lerr := database.ListSessionsByUserId(ctx, stranger.ID); lerr != nil || len(rows) != 1 {
		t.Fatalf("foreign session row must survive another user's revoke: %v, %d rows", lerr, len(rows))
	}

	// Unknown id: also NotFound.
	_, err = client.RevokeSession(ctx, cookiedRequest(t, &supervisorv1.RevokeSessionRequest{SessionId: 999999}, mine))
	if err == nil || connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown session revoke = %v, want NotFound", err)
	}

	// Revoking the CURRENT session behaves like logout: cookie cleared,
	// token dead.
	sess, err := database.GetSessionByTokenHash(ctx, server.HashToken(mine))
	if err != nil {
		t.Fatalf("GetSessionByTokenHash: %v", err)
	}
	res, err := client.RevokeSession(ctx, cookiedRequest(t, &supervisorv1.RevokeSessionRequest{SessionId: sess.ID}, mine))
	if err != nil {
		t.Fatalf("revoke current: %v", err)
	}
	requireExpiringCookie(t, res.Header().Get("Set-Cookie"))
	if _, err := getSessionWithCookie(t, client, mine); err == nil || connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("current session survived self-revoke: %v", err)
	}
}

func TestRevokeOtherSessionsKeepsCurrent(t *testing.T) {
	ctx := context.Background()
	database := setupTestDB(t)
	_, client := sessionTestServer(t, database, testSessionConfig())
	setupAdmin(t, client)

	loginWithUA(t, client, "Mozilla/5.0 (X11; Linux x86_64; rv:130.0) Gecko/20100101 Firefox/130.0")
	loginWithUA(t, client, "python-requests/2.31.0")
	keep := loginWithUA(t, client, "curl/8.5.0")

	res, err := client.RevokeOtherSessions(ctx, cookiedRequest(t, &supervisorv1.RevokeOtherSessionsRequest{}, keep))
	if err != nil {
		t.Fatalf("RevokeOtherSessions: %v", err)
	}
	// SetupAdmin's session plus the two other logins.
	if res.Msg.Revoked != 3 {
		t.Fatalf("revoked = %d, want 3", res.Msg.Revoked)
	}

	list, err := client.ListSessions(ctx, cookiedRequest(t, &supervisorv1.ListSessionsRequest{}, keep))
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list.Msg.Sessions) != 1 || !list.Msg.Sessions[0].IsCurrent {
		t.Fatalf("want only the current session to survive: %+v", list.Msg.Sessions)
	}
	if list.Msg.Sessions[0].DeviceLabel != "curl 8.5.0" {
		t.Fatalf("surviving session = %q, want the curl one", list.Msg.Sessions[0].DeviceLabel)
	}

	logs, err := database.ListAuditLogs(ctx, db.ListAuditLogsParams{Limit: 50, Offset: 0})
	if err != nil {
		t.Fatalf("ListAuditLogs: %v", err)
	}
	found := false
	for i := range logs {
		if logs[i].Action == "auth.session_revoked" && strings.Contains(logs[i].Details.String, `"scope":"others"`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("auth.session_revoked (scope others) audit row missing; actions: %v", auditActions(logs))
	}
}
