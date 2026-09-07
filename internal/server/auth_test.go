package server_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/noosxe/runnero/internal/db"
	"github.com/noosxe/runnero/internal/keys"
	supervisorv1 "github.com/noosxe/runnero/internal/pb/supervisor/v1"
	"github.com/noosxe/runnero/internal/pb/supervisor/v1/supervisorv1connect"
	"github.com/noosxe/runnero/internal/server"
)

func setupTestDB(t *testing.T) (*db.DB, []byte) {
	t.Helper()
	derived, err := keys.Derive("01234567890123456789012345678901")
	if err != nil {
		t.Fatalf("Derive keys failed: %v", err)
	}

	database, err := db.Open(db.Options{
		Path:          ":memory:",
		EncryptionKey: derived.DBEncryptionKey,
	})
	if err != nil {
		t.Fatalf("db.Open failed: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	return database, derived.JWTSigningSecret
}

func TestAuthEngineFullLifecycleAndRevocation(t *testing.T) {
	ctx := context.Background()
	database, jwtSecret := setupTestDB(t)

	srv := server.New(server.Options{
		Port:             8080,
		AuthDB:           database,
		JWTSigningSecret: jwtSecret,
	})

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	client := supervisorv1connect.NewAuthServiceClient(ts.Client(), ts.URL)

	// 1. SetupAdmin with empty fields fails with InvalidArgument
	_, err := client.SetupAdmin(ctx, connect.NewRequest(&supervisorv1.SetupAdminRequest{
		Username: "",
		Password: "",
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("SetupAdmin empty fields want CodeInvalidArgument, got: %v", err)
	}

	// 2. SetupAdmin first-run succeeds
	setupRes, err := client.SetupAdmin(ctx, connect.NewRequest(&supervisorv1.SetupAdminRequest{
		Username: "admin",
		Password: "super-secret-password-123",
	}))
	if err != nil {
		t.Fatalf("SetupAdmin failed: %v", err)
	}
	if !setupRes.Msg.Success {
		t.Errorf("SetupAdmin want success=true, got: %v", setupRes.Msg.Success)
	}

	setupCookie := setupRes.Header().Get("Set-Cookie")
	if !strings.Contains(setupCookie, "session_token=") || !strings.Contains(setupCookie, "HttpOnly") || !strings.Contains(setupCookie, "SameSite=Strict") {
		t.Fatalf("SetupAdmin Set-Cookie header missing required security directives: %s", setupCookie)
	}

	// Verify session row exists in DB from SetupAdmin
	rawSetupCookie := strings.Split(strings.Split(setupCookie, ";")[0], "=")[1]
	setupTokenHash := server.HashToken(rawSetupCookie)
	setupSess, err := database.GetSessionByTokenHash(ctx, setupTokenHash)
	if err != nil {
		t.Fatalf("GetSessionByTokenHash after SetupAdmin failed: %v", err)
	}
	if setupSess.TokenHash != setupTokenHash {
		t.Errorf("token_hash mismatch: got %s, want %s", setupSess.TokenHash, setupTokenHash)
	}

	// Verify audit log for admin setup
	auditLogs, err := database.ListAuditLogs(ctx, db.ListAuditLogsParams{Limit: 10, Offset: 0})
	if err != nil || len(auditLogs) != 1 || auditLogs[0].Action != "auth.setup_admin" {
		t.Fatalf("expected 1 audit log for auth.setup_admin, got %+v (err %v)", auditLogs, err)
	}

	// 3. SetupAdmin second time fails with FailedPrecondition
	_, err = client.SetupAdmin(ctx, connect.NewRequest(&supervisorv1.SetupAdminRequest{
		Username: "admin2",
		Password: "password123",
	}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("SetupAdmin second call want CodeFailedPrecondition, got: %v", err)
	}

	// 4. Login with invalid password fails with Unauthenticated and records audit log
	_, err = client.Login(ctx, connect.NewRequest(&supervisorv1.LoginRequest{
		Username: "admin",
		Password: "wrong-password",
	}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("Login wrong password want CodeUnauthenticated, got: %v", err)
	}

	auditLogs, err = database.ListAuditLogs(ctx, db.ListAuditLogsParams{Limit: 10, Offset: 0})
	if err != nil || len(auditLogs) != 2 || auditLogs[0].Action != "auth.login_failed" {
		t.Fatalf("expected audit log for auth.login_failed, got %+v", auditLogs)
	}

	// 5. Login with correct credentials succeeds and returns Set-Cookie header
	loginReq := connect.NewRequest(&supervisorv1.LoginRequest{
		Username: "admin",
		Password: "super-secret-password-123",
	})
	loginRes, err := client.Login(ctx, loginReq)
	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}
	if !loginRes.Msg.Success || loginRes.Msg.Username != "admin" {
		t.Fatalf("unexpected LoginResponse: %+v", loginRes.Msg)
	}

	setCookie := loginRes.Header().Get("Set-Cookie")
	if !strings.Contains(setCookie, "session_token=") || !strings.Contains(setCookie, "HttpOnly") || !strings.Contains(setCookie, "SameSite=Strict") {
		t.Fatalf("Set-Cookie header missing required security directives: %s", setCookie)
	}

	// Extract raw session token value
	rawCookie := strings.Split(strings.Split(setCookie, ";")[0], "=")[1]

	// Verify session row exists in DB
	tokenHash := server.HashToken(rawCookie)
	sess, err := database.GetSessionByTokenHash(ctx, tokenHash)
	if err != nil {
		t.Fatalf("GetSessionByTokenHash failed: %v", err)
	}
	if sess.TokenHash != tokenHash {
		t.Errorf("token_hash mismatch: got %s, want %s", sess.TokenHash, tokenHash)
	}

	// 6. GetSession without cookie fails
	_, err = client.GetSession(ctx, connect.NewRequest(&supervisorv1.GetSessionRequest{}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("GetSession without cookie want CodeUnauthenticated, got: %v", err)
	}

	// 7. GetSession with valid cookie succeeds (round-trip verification)
	getReq := connect.NewRequest(&supervisorv1.GetSessionRequest{})
	getReq.Header().Set("Cookie", "session_token="+rawCookie)
	getRes, err := client.GetSession(ctx, getReq)
	if err != nil {
		t.Fatalf("GetSession with cookie failed: %v", err)
	}
	if getRes.Msg.Username != "admin" || !getRes.Msg.IsAdmin {
		t.Errorf("unexpected GetSession response: %+v", getRes.Msg)
	}
	if getRes.Msg.HostArch != server.HostArch() || getRes.Msg.HostOs != server.HostOS() {
		t.Errorf("expected host arch/os %s/%s, got %s/%s", server.HostArch(), server.HostOS(), getRes.Msg.HostArch, getRes.Msg.HostOs)
	}

	// 8. Revoked session: delete session from database, then GetSession must fail
	if err := database.DeleteSessionByTokenHash(ctx, tokenHash); err != nil {
		t.Fatalf("DeleteSessionByTokenHash failed: %v", err)
	}

	_, err = client.GetSession(ctx, getReq)
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("GetSession with revoked session want CodeUnauthenticated, got: %v", err)
	}
}

type mockProtectedPoolService struct {
	supervisorv1connect.UnimplementedPoolServiceHandler
}

func (m *mockProtectedPoolService) ListPools(ctx context.Context, req *connect.Request[supervisorv1.ListPoolsRequest]) (*connect.Response[supervisorv1.ListPoolsResponse], error) {
	user, ok := server.GetUserContext(ctx)
	if !ok || user.Username == "" {
		return nil, connect.NewError(connect.CodeInternal, errors.New("user context missing"))
	}
	return connect.NewResponse(&supervisorv1.ListPoolsResponse{}), nil
}

func TestAuthInterceptorProtectedEndpoints(t *testing.T) {
	ctx := context.Background()
	database, jwtSecret := setupTestDB(t)

	srv := server.New(server.Options{
		Port:             8080,
		AuthDB:           database,
		JWTSigningSecret: jwtSecret,
	})

	// Mount a protected service with srv.ConnectHandlerOptions()
	poolPath, poolHandler := supervisorv1connect.NewPoolServiceHandler(&mockProtectedPoolService{}, srv.ConnectHandlerOptions()...)
	srv.MountConnectHandler(poolPath, poolHandler)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	poolClient := supervisorv1connect.NewPoolServiceClient(ts.Client(), ts.URL)

	// 1. Calling protected endpoint without session cookie fails with CodeUnauthenticated
	_, err := poolClient.ListPools(ctx, connect.NewRequest(&supervisorv1.ListPoolsRequest{}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("expected unauthenticated request to fail with CodeUnauthenticated, got: %v", err)
	}

	// 2. Setup and login to obtain valid cookie
	authClient := supervisorv1connect.NewAuthServiceClient(ts.Client(), ts.URL)
	_, err = authClient.SetupAdmin(ctx, connect.NewRequest(&supervisorv1.SetupAdminRequest{
		Username: "admin",
		Password: "password123",
	}))
	if err != nil {
		t.Fatalf("SetupAdmin failed: %v", err)
	}

	loginRes, err := authClient.Login(ctx, connect.NewRequest(&supervisorv1.LoginRequest{
		Username: "admin",
		Password: "password123",
	}))
	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}

	setCookie := loginRes.Header().Get("Set-Cookie")
	rawCookie := strings.Split(strings.Split(setCookie, ";")[0], "=")[1]

	// 3. Calling protected endpoint with valid cookie succeeds and accesses UserContext
	req := connect.NewRequest(&supervisorv1.ListPoolsRequest{})
	req.Header().Set("Cookie", "session_token="+rawCookie)
	res, err := poolClient.ListPools(ctx, req)
	if err != nil {
		t.Fatalf("calling protected endpoint with valid session cookie failed: %v", err)
	}
	if res.Msg == nil {
		t.Fatalf("expected response message")
	}

	// 4. Revoking the session causes protected endpoint to reject subsequent calls
	tokenHash := server.HashToken(rawCookie)
	if err := database.DeleteSessionByTokenHash(ctx, tokenHash); err != nil {
		t.Fatalf("failed to delete session: %v", err)
	}

	_, err = poolClient.ListPools(ctx, req)
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("expected revoked session to be rejected on protected endpoint, got: %v", err)
	}
}
