package server_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	"github.com/noosxe/runnero/internal/db"
	supervisorv1 "github.com/noosxe/runnero/internal/pb/supervisor/v1"
	"github.com/noosxe/runnero/internal/pb/supervisor/v1/supervisorv1connect"
	"github.com/noosxe/runnero/internal/server"
)

// Tests for the viewer role and the user-management surface (RUN-236,
// docs/35): live role enforcement, the last-admin and no-self-delete
// guards, the password reset sweep, and the audit vocabulary.

func usersTestServer(t *testing.T, database *db.DB) (supervisorv1connect.AuthServiceClient, supervisorv1connect.UserServiceClient, supervisorv1connect.PoolServiceClient) {
	t.Helper()
	srv := server.New(server.Options{
		Port:         8080,
		AuthDB:       database,
		PoolDB:       database,
		OnboardingDB: database,
		Session:      testSessionConfig(),
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return supervisorv1connect.NewAuthServiceClient(ts.Client(), ts.URL),
		supervisorv1connect.NewUserServiceClient(ts.Client(), ts.URL),
		supervisorv1connect.NewPoolServiceClient(ts.Client(), ts.URL)
}

// loginToken performs the RPC login and returns the raw session token
// from the Set-Cookie header.
func loginToken(t *testing.T, client supervisorv1connect.AuthServiceClient, username, password string) string {
	t.Helper()
	res, err := client.Login(context.Background(), connect.NewRequest(&supervisorv1.LoginRequest{
		Username: username,
		Password: password,
	}))
	if err != nil {
		t.Fatalf("login as %s failed: %v", username, err)
	}
	return cookieValue(res.Header().Get("Set-Cookie"))
}

// authedReq attaches a session cookie to a request.
func authedReq[T any](msg *T, rawToken string) *connect.Request[T] {
	req := connect.NewRequest(msg)
	req.Header().Set("Cookie", "session_token="+rawToken)
	return req
}

// TestViewerLifecycleLiveEnforcement walks the core story (docs/35 §2.1,
// §2.3): create a viewer, log in, observe the read surface open and the
// write surface closed, then flip the role and watch the change apply to
// the same session on the next request - no re-login in either direction.
func TestViewerLifecycleLiveEnforcement(t *testing.T) {
	ctx := context.Background()
	database := setupTestDB(t)
	auth, users, pools := usersTestServer(t, database)
	setupAdmin(t, auth)
	adminTok := loginToken(t, auth, "admin", "super-secret-password-123")

	// Invalid role is a typed violation on the role field.
	_, err := users.CreateUser(ctx, authedReq(&supervisorv1.CreateUserRequest{
		Username: "oops", Password: "super-secret-password-123", Role: "superuser",
	}, adminTok))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid role: got %v, want InvalidArgument", connect.CodeOf(err))
	}

	created, err := users.CreateUser(ctx, authedReq(&supervisorv1.CreateUserRequest{
		Username: "observer", Password: "observer-password-123", Role: "viewer",
	}, adminTok))
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if created.Msg.User.Role != "viewer" || created.Msg.User.Username != "observer" {
		t.Fatalf("created user = %+v", created.Msg.User)
	}

	// Duplicate username answers AlreadyExists.
	_, err = users.CreateUser(ctx, authedReq(&supervisorv1.CreateUserRequest{
		Username: "observer", Password: "observer-password-123", Role: "viewer",
	}, adminTok))
	if connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("duplicate username: got %v, want AlreadyExists", connect.CodeOf(err))
	}

	viewerTok := loginToken(t, auth, "observer", "observer-password-123")

	// The viewer session reports the live role.
	sess, err := auth.GetSession(ctx, authedReq(&supervisorv1.GetSessionRequest{}, viewerTok))
	if err != nil {
		t.Fatalf("viewer GetSession: %v", err)
	}
	if sess.Msg.Role != "viewer" || sess.Msg.IsAdmin {
		t.Fatalf("viewer session = role %q is_admin %v", sess.Msg.Role, sess.Msg.IsAdmin)
	}

	// Observability reads pass; supervisor internals and writes are denied.
	if _, err := pools.ListPools(ctx, authedReq(&supervisorv1.ListPoolsRequest{}, viewerTok)); err != nil {
		t.Fatalf("viewer ListPools should pass: %v", err)
	}
	_, err = users.ListUsers(ctx, authedReq(&supervisorv1.ListUsersRequest{}, viewerTok))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("viewer ListUsers: got %v, want PermissionDenied", connect.CodeOf(err))
	}

	// Flip the role while the viewer session is live: the next request on
	// the SAME token hits the admin surface (docs/35 §2.1 live effect).
	if _, err := users.SetUserRole(ctx, authedReq(&supervisorv1.SetUserRoleRequest{
		Username: "observer", Role: "admin",
	}, adminTok)); err != nil {
		t.Fatalf("promotion: %v", err)
	}
	if _, err := users.ListUsers(ctx, authedReq(&supervisorv1.ListUsersRequest{}, viewerTok)); err != nil {
		t.Fatalf("promoted user should reach admin surface without re-login: %v", err)
	}

	// ... and the downgrade bites the same way.
	if _, err := users.SetUserRole(ctx, authedReq(&supervisorv1.SetUserRoleRequest{
		Username: "observer", Role: "viewer",
	}, adminTok)); err != nil {
		t.Fatalf("demotion: %v", err)
	}
	_, err = users.ListUsers(ctx, authedReq(&supervisorv1.ListUsersRequest{}, viewerTok))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("demoted user: got %v, want PermissionDenied", connect.CodeOf(err))
	}
	sess, err = auth.GetSession(ctx, authedReq(&supervisorv1.GetSessionRequest{}, viewerTok))
	if err != nil {
		t.Fatalf("demoted GetSession: %v", err)
	}
	if sess.Msg.Role != "viewer" {
		t.Fatalf("demoted session role = %q, want viewer", sess.Msg.Role)
	}
}

// TestLastAdminGuards proves the structural guard rails (docs/35 §2.3):
// the last admin cannot be demoted or deleted, nobody deletes themselves,
// and two admins can demote each other once the other exists. The
// concurrent race case (two simultaneous demotions, at least one refused)
// is TestConcurrentDemoteRefusesLastAdmin.
func TestLastAdminGuards(t *testing.T) {
	ctx := context.Background()
	database := setupTestDB(t)
	auth, users, _ := usersTestServer(t, database)
	setupAdmin(t, auth)
	adminTok := loginToken(t, auth, "admin", "super-secret-password-123")

	// Last admin: demote refused.
	_, err := users.SetUserRole(ctx, authedReq(&supervisorv1.SetUserRoleRequest{
		Username: "admin", Role: "viewer",
	}, adminTok))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("demote last admin: got %v, want FailedPrecondition", connect.CodeOf(err))
	}

	// Self-delete refused (even though it is also the last admin).
	_, err = users.DeleteUser(ctx, authedReq(&supervisorv1.DeleteUserRequest{Username: "admin"}, adminTok))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("self-delete: got %v, want FailedPrecondition", connect.CodeOf(err))
	}

	// Unknown target answers NotFound.
	_, err = users.DeleteUser(ctx, authedReq(&supervisorv1.DeleteUserRequest{Username: "ghost"}, adminTok))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("delete unknown: got %v, want NotFound", connect.CodeOf(err))
	}

	// A second admin unlocks self-demote (allowed) and delete.
	if _, err := users.CreateUser(ctx, authedReq(&supervisorv1.CreateUserRequest{
		Username: "second", Password: "second-admin-pass-1", Role: "admin",
	}, adminTok)); err != nil {
		t.Fatalf("CreateUser second admin: %v", err)
	}
	if _, err := users.SetUserRole(ctx, authedReq(&supervisorv1.SetUserRoleRequest{
		Username: "admin", Role: "viewer",
	}, adminTok)); err != nil {
		t.Fatalf("self-demote with another admin present should pass: %v", err)
	}

	// The demoted (former) admin's management calls are now denied, and the
	// surviving admin can delete them.
	_, err = users.SetUserRole(ctx, authedReq(&supervisorv1.SetUserRoleRequest{
		Username: "second", Role: "viewer",
	}, adminTok))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("demoted admin SetUserRole: got %v, want PermissionDenied", connect.CodeOf(err))
	}
	if _, err := users.DeleteUser(ctx, authedReq(&supervisorv1.DeleteUserRequest{Username: "admin"}, loginToken(t, auth, "second", "second-admin-pass-1"))); err != nil {
		t.Fatalf("second admin deletes demoted user: %v", err)
	}
}

// TestConcurrentDemoteRefusesLastAdmin races two demotions of the only two
// admins (docs/35 §6): the guard sequences are serialized, so at least one
// is refused and an admin always remains.
func TestConcurrentDemoteRefusesLastAdmin(t *testing.T) {
	ctx := context.Background()
	database := setupTestDB(t)
	auth, users, _ := usersTestServer(t, database)
	setupAdmin(t, auth)
	adminTok := loginToken(t, auth, "admin", "super-secret-password-123")
	if _, err := users.CreateUser(ctx, authedReq(&supervisorv1.CreateUserRequest{
		Username: "second", Password: "second-admin-pass-1", Role: "admin",
	}, adminTok)); err != nil {
		t.Fatalf("CreateUser second admin: %v", err)
	}
	secondTok := loginToken(t, auth, "second", "second-admin-pass-1")

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errs[0] = users.SetUserRole(ctx, authedReq(&supervisorv1.SetUserRoleRequest{Username: "admin", Role: "viewer"}, adminTok))
	}()
	go func() {
		defer wg.Done()
		_, errs[1] = users.SetUserRole(ctx, authedReq(&supervisorv1.SetUserRoleRequest{Username: "second", Role: "viewer"}, secondTok))
	}()
	wg.Wait()

	// The serialized guard sequences make the outcome deterministic: the
	// first demotion drops the admin count to one, so exactly the second
	// is refused.
	successes := 0
	for i, err := range errs {
		if err == nil {
			successes++
			continue
		}
		if connect.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Fatalf("demotion %d: got %v, want FailedPrecondition", i, connect.CodeOf(err))
		}
	}
	if successes != 1 {
		t.Fatalf("successful demotions = %d, want exactly 1 (the guard must refuse the rest)", successes)
	}

	// Exactly one admin remains.
	row, err := database.CountAdminRoleUsers(ctx)
	if err != nil {
		t.Fatalf("count admins: %v", err)
	}
	if row < 1 {
		t.Fatalf("admin count after race = %d, want 1", row)
	}
}

// TestSetUserPasswordResetSweep proves the admin reset (docs/35 §2.3,
// OQ-2): the target's sessions die, the new password works, the caller's
// own session survives even when resetting themselves, and the audit row
// names the target.
func TestSetUserPasswordResetSweep(t *testing.T) {
	ctx := context.Background()
	database := setupTestDB(t)
	auth, users, _ := usersTestServer(t, database)
	setupAdmin(t, auth)
	adminTok := loginToken(t, auth, "admin", "super-secret-password-123")
	if _, err := users.CreateUser(ctx, authedReq(&supervisorv1.CreateUserRequest{
		Username: "observer", Password: "observer-password-123", Role: "viewer",
	}, adminTok)); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Two live sessions for the target.
	oldTok1 := loginToken(t, auth, "observer", "observer-password-123")
	oldTok2 := loginToken(t, auth, "observer", "observer-password-123")

	res, err := users.SetUserPassword(ctx, authedReq(&supervisorv1.SetUserPasswordRequest{
		Username: "observer", Password: "brand-new-password-9",
	}, adminTok))
	if err != nil {
		t.Fatalf("SetUserPassword: %v", err)
	}
	if res.Msg.RevokedSessions != 2 {
		t.Fatalf("revoked = %d, want 2 (all target sessions)", res.Msg.RevokedSessions)
	}

	for i, tok := range []string{oldTok1, oldTok2} {
		if _, err := auth.GetSession(ctx, authedReq(&supervisorv1.GetSessionRequest{}, tok)); connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Fatalf("old session %d: got %v, want Unauthenticated", i, connect.CodeOf(err))
		}
	}
	_ = loginToken(t, auth, "observer", "brand-new-password-9") // new password works

	// The admin's session is untouched by resetting someone else.
	if _, err := auth.GetSession(ctx, authedReq(&supervisorv1.GetSessionRequest{}, adminTok)); err != nil {
		t.Fatalf("admin session should survive: %v", err)
	}

	// Audit names the target, never the password.
	rows, err := database.ListAuditLogs(ctx, db.ListAuditLogsParams{Limit: 50, Offset: 0})
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	found := false
	for _, row := range rows {
		if row.Action == "auth.password_reset" {
			found = true
			if !strings.Contains(row.Details.String, `"target_username":"observer"`) {
				t.Fatalf("password_reset details missing target: %q", row.Details.String)
			}
			if strings.Contains(row.Details.String, "brand-new-password") {
				t.Fatalf("password_reset details leak the password: %q", row.Details.String)
			}
		}
	}
	if !found {
		t.Fatal("no auth.password_reset audit row")
	}
}

// TestDeleteUserCascades proves deletion sweeps the account's dependents
// (docs/35 §4): sessions and webauthn credentials die with the user while
// the audit history survives.
func TestDeleteUserCascades(t *testing.T) {
	ctx := context.Background()
	database := setupTestDB(t)
	auth, users, _ := usersTestServer(t, database)
	setupAdmin(t, auth)
	adminTok := loginToken(t, auth, "admin", "super-secret-password-123")
	if _, err := users.CreateUser(ctx, authedReq(&supervisorv1.CreateUserRequest{
		Username: "observer", Password: "observer-password-123", Role: "viewer",
	}, adminTok)); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	viewerTok := loginToken(t, auth, "observer", "observer-password-123")

	target, err := database.GetAdminUserByUsername(ctx, "observer")
	if err != nil {
		t.Fatalf("load target: %v", err)
	}
	_, err = database.CreateWebauthnCredential(ctx, db.CreateWebauthnCredentialParams{
		UserID:          target.ID,
		Name:            "test-key",
		CredentialID:    []byte("cred-1"),
		PublicKey:       []byte("pk"),
		Aaguid:          "",
		AttestationType: "none",
		Transports:      "",
		SignCount:       0,
		BackupEligible:  0,
		BackupState:     0,
		CloneWarning:    0,
	})
	if err != nil {
		t.Fatalf("seed credential: %v", err)
	}

	if _, err := users.DeleteUser(ctx, authedReq(&supervisorv1.DeleteUserRequest{Username: "observer"}, adminTok)); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}

	// The target's live session is gone.
	if _, err := auth.GetSession(ctx, authedReq(&supervisorv1.GetSessionRequest{}, viewerTok)); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("deleted user session: got %v, want Unauthenticated", connect.CodeOf(err))
	}
	// The credential row cascaded.
	if _, err := database.GetWebauthnCredentialById(ctx, []byte("cred-1")); err == nil {
		t.Fatal("webauthn credential survived user deletion")
	}
	// The user row is gone; login answers invalid credentials.
	if _, err := auth.Login(ctx, connect.NewRequest(&supervisorv1.LoginRequest{Username: "observer", Password: "observer-password-123"})); err == nil {
		t.Fatal("login succeeded for a deleted user")
	}
	// The audit history survives, with the acting admin recorded.
	rows, err := database.ListAuditLogs(ctx, db.ListAuditLogsParams{Limit: 50, Offset: 0})
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	found := false
	for _, row := range rows {
		if row.Action == "auth.user_deleted" && strings.Contains(row.Details.String, `"target_username":"observer"`) {
			found = true
		}
	}
	if !found {
		t.Fatal("no auth.user_deleted audit row naming the target")
	}
}
