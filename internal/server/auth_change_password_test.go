package server_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/noosxe/runnero/internal/db"
	supervisorv1 "github.com/noosxe/runnero/internal/pb/supervisor/v1"
	supervisorv1connect "github.com/noosxe/runnero/internal/pb/supervisor/v1/supervisorv1connect"
)

// loginFor returns the raw session token for a fresh login (RUN-237 helpers).
func loginFor(t *testing.T, client supervisorv1connect.AuthServiceClient, password string) string {
	t.Helper()
	res, err := client.Login(context.Background(), connect.NewRequest(&supervisorv1.LoginRequest{
		Username: "admin",
		Password: password,
	}))
	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}
	return cookieValue(res.Header().Get("Set-Cookie"))
}

// changePasswordWithCookie calls ChangePassword authenticated by rawCookie.
func changePasswordWithCookie(client supervisorv1connect.AuthServiceClient, rawCookie, current, next string) (*connect.Response[supervisorv1.ChangePasswordResponse], error) {
	req := connect.NewRequest(&supervisorv1.ChangePasswordRequest{
		CurrentPassword: current,
		NewPassword:     next,
	})
	req.Header().Set("Cookie", "session_token="+rawCookie)
	return client.ChangePassword(context.Background(), req)
}

// TestChangePasswordRequiresSession: the RPC is session-gated (bucketAdmin).
func TestChangePasswordRequiresSession(t *testing.T) {
	database := setupTestDB(t)
	_, client := sessionTestServer(t, database, testSessionConfig())
	setupAdmin(t, client)

	_, err := client.ChangePassword(context.Background(), connect.NewRequest(&supervisorv1.ChangePasswordRequest{
		CurrentPassword: "super-secret-password-123",
		NewPassword:     "brand-new-password-456",
	}))
	if err == nil {
		t.Fatal("unauthenticated ChangePassword must fail")
	}
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("code = %v, want Unauthenticated", connect.CodeOf(err))
	}
}

// TestChangePasswordShortNewPasswordRejected pins the class-C 12-character
// floor (docs/32 §4.4) as a server-side declarative rule, not just UI.
func TestChangePasswordShortNewPasswordRejected(t *testing.T) {
	database := setupTestDB(t)
	_, client := sessionTestServer(t, database, testSessionConfig())
	setupAdmin(t, client)
	cookie := loginFor(t, client, "super-secret-password-123")

	_, err := changePasswordWithCookie(client, cookie, "super-secret-password-123", "short12char")
	if err == nil {
		t.Fatal("11-character new password must be rejected")
	}
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", connect.CodeOf(err))
	}
}

// TestChangePasswordWrongCurrentPinsField: a wrong current password fails
// with a field-attached violation so form clients mark the field, the
// password row stays untouched, and the failure is audited.
func TestChangePasswordWrongCurrentPinsField(t *testing.T) {
	ctx := context.Background()
	database := setupTestDB(t)
	_, client := sessionTestServer(t, database, testSessionConfig())
	setupAdmin(t, client)
	cookie := loginFor(t, client, "super-secret-password-123")

	before, err := database.GetAdminUserByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("load user: %v", err)
	}

	_, err = changePasswordWithCookie(client, cookie, "totally-wrong-guess", "brand-new-password-456")
	if err == nil {
		t.Fatal("wrong current password must fail")
	}
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", connect.CodeOf(err))
	}
	violations := violationsFromError(t, err)
	if len(violations.GetViolations()) != 1 {
		t.Fatalf("want exactly 1 violation, got %d", len(violations.GetViolations()))
	}
	v := violations.GetViolations()[0]
	if v.GetRuleId() != "auth.password.current_mismatch" {
		t.Fatalf("rule id = %q, want auth.password.current_mismatch", v.GetRuleId())
	}
	if lastFieldName(v) != "current_password" {
		t.Fatalf("field = %q, want current_password", lastFieldName(v))
	}

	// The stored hash is untouched: the old password still logs in.
	_ = loginFor(t, client, "super-secret-password-123")

	after, err := database.GetAdminUserByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if after.PasswordHash != before.PasswordHash {
		t.Fatal("wrong-current-password attempt must not change the stored hash")
	}

	// Audited with the coarse reason, like login failures (docs/32 §5.1).
	logs, lerr := database.ListAuditLogs(ctx, db.ListAuditLogsParams{Limit: 50, Offset: 0})
	if lerr != nil {
		t.Fatalf("ListAuditLogs: %v", lerr)
	}
	found := false
	for _, row := range logs {
		if row.Action == "auth.password_change_failed" {
			found = true
			if !row.SourceIp.Valid {
				t.Fatal("password_change_failed audit row must carry source_ip")
			}
		}
	}
	if !found {
		t.Fatalf("auth.password_change_failed not audited; actions: %v", auditActions(logs))
	}
}

// TestChangePasswordSuccessRevokesOtherSessions is the RUN-237 core flow:
// the hash rotates under the class-C floor, every OTHER session dies, the
// current session survives, the new password logs in, and the change is
// audited with source_ip.
func TestChangePasswordSuccessRevokesOtherSessions(t *testing.T) {
	ctx := context.Background()
	database := setupTestDB(t)
	ts, client := sessionTestServer(t, database, testSessionConfig())
	setupAdmin(t, client)

	// Two concurrent sessions: the changer (current) and a bystander device.
	currentCookie := loginFor(t, client, "super-secret-password-123")
	otherCookie := loginFor(t, client, "super-secret-password-123")

	res, err := changePasswordWithCookie(client, currentCookie, "super-secret-password-123", "brand-new-password-456")
	if err != nil {
		t.Fatalf("ChangePassword failed: %v", err)
	}
	if !res.Msg.Success {
		t.Fatal("success flag not set")
	}
	if res.Msg.RevokedSessions != 2 {
		// SetupAdmin's bootstrap session + the bystander login.
		t.Fatalf("revoked = %d, want 2 (bootstrap + bystander)", res.Msg.RevokedSessions)
	}

	// The bystander session is dead server-side.
	if _, gerr := getSessionWithCookie(t, client, otherCookie); gerr == nil ||
		connect.CodeOf(gerr) != connect.CodeUnauthenticated {
		t.Fatalf("other session must be unauthenticated after change, got %v", gerr)
	}

	// The current session survives: the caller stays signed in.
	if _, gerr := getSessionWithCookie(t, client, currentCookie); gerr != nil {
		t.Fatalf("current session must survive the change: %v", gerr)
	}

	// New password logs in, old password is refused.
	_ = loginFor(t, client, "brand-new-password-456")
	if _, lerr := client.Login(ctx, connect.NewRequest(&supervisorv1.LoginRequest{
		Username: "admin",
		Password: "super-secret-password-123",
	})); lerr == nil {
		t.Fatal("old password must not log in after the change")
	}

	// Session count: the current row plus the fresh new-password login.
	sessions, serr := database.ListSessionsByUserId(ctx, 1)
	if serr != nil {
		t.Fatalf("ListSessionsByUserId: %v", serr)
	}
	if len(sessions) != 2 {
		t.Fatalf("want 2 surviving session rows (current + new-password login), got %d", len(sessions))
	}

	// Audited as auth.password_changed with source_ip.
	logs, lerr := database.ListAuditLogs(ctx, db.ListAuditLogsParams{Limit: 50, Offset: 0})
	if lerr != nil {
		t.Fatalf("ListAuditLogs: %v", lerr)
	}
	found := false
	for _, row := range logs {
		if row.Action == "auth.password_changed" {
			found = true
			if !row.SourceIp.Valid {
				t.Fatal("password_changed audit row must carry source_ip")
			}
		}
	}
	if !found {
		t.Fatalf("auth.password_changed not audited; actions: %v", auditActions(logs))
	}

	_ = ts // kept alive until test end
}

// TestChangePasswordVerifiesFreshHash pins that a second, sequential change
// verifies against the freshly stored hash: after one rotation, the NEW
// password works as current_password and the original no longer does.
func TestChangePasswordVerifiesFreshHash(t *testing.T) {
	database := setupTestDB(t)
	_, client := sessionTestServer(t, database, testSessionConfig())
	setupAdmin(t, client)
	cookie := loginFor(t, client, "super-secret-password-123")

	// Change once, then change again using the NEW password as current: the
	// second attempt must verify against the fresh hash, not a cached one.
	if _, err := changePasswordWithCookie(client, cookie, "super-secret-password-123", "second-password-789"); err != nil {
		t.Fatalf("first change failed: %v", err)
	}
	res, err := changePasswordWithCookie(client, cookie, "second-password-789", "third-password-000")
	if err != nil {
		t.Fatalf("sequential change must verify against the fresh hash: %v", err)
	}
	if !res.Msg.Success {
		t.Fatal("second change success flag not set")
	}
}
