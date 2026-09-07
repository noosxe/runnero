package server_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/noosxe/runnero/internal/db"
	supervisorv1 "github.com/noosxe/runnero/internal/pb/supervisor/v1"
	"github.com/noosxe/runnero/internal/pb/supervisor/v1/supervisorv1connect"
	"github.com/noosxe/runnero/internal/server"
)

type mockValidator struct {
	shouldFail bool
}

func (m *mockValidator) ValidateCredentials(_ context.Context, req *supervisorv1.CreateAuthProfileRequest) error {
	if m.shouldFail {
		return errors.New("upstream git provider rejected token")
	}
	return nil
}

func TestAuthProfileServiceCRUDAndSecurity(t *testing.T) {
	ctx := context.Background()
	database, jwtSecret := setupTestDB(t)

	// Derive a valid 32-byte AES encryption key
	dbEncKey := []byte("01234567890123456789012345678901") // 32 bytes

	validator := &mockValidator{}

	srv := server.New(server.Options{
		Port:                8080,
		AuthDB:              database,
		PoolDB:              database,
		AuthProfileDB:       database,
		DBEncryptionKey:     dbEncKey,
		CredentialValidator: validator,
		JWTSigningSecret:    jwtSecret,
	})

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 1. Authenticate admin
	authClient := supervisorv1connect.NewAuthServiceClient(ts.Client(), ts.URL)
	_, err := authClient.SetupAdmin(ctx, connect.NewRequest(&supervisorv1.SetupAdminRequest{
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
	cookie := loginRes.Header().Get("Set-Cookie")
	rawCookie := strings.Split(strings.Split(cookie, ";")[0], "=")[1]

	client := supervisorv1connect.NewAuthProfileServiceClient(ts.Client(), ts.URL)

	// 2. Validation: unsupported auth_method
	badMethodReq := connect.NewRequest(&supervisorv1.CreateAuthProfileRequest{
		Name:       "bad-auth",
		AuthMethod: "oauth1",
		Token:      "some-token",
	})
	badMethodReq.Header().Set("Cookie", "session_token="+rawCookie)
	_, err = client.CreateAuthProfile(ctx, badMethodReq)
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("unsupported auth_method want CodeInvalidArgument, got: %v", err)
	}

	// 3. Validation: missing token for PAT
	missingTokenReq := connect.NewRequest(&supervisorv1.CreateAuthProfileRequest{
		Name:       "pat-missing",
		AuthMethod: "pat",
	})
	missingTokenReq.Header().Set("Cookie", "session_token="+rawCookie)
	_, err = client.CreateAuthProfile(ctx, missingTokenReq)
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("missing token want CodeInvalidArgument, got: %v", err)
	}

	// 4. Validation: missing private_key or app_id for github_app
	missingAppReq := connect.NewRequest(&supervisorv1.CreateAuthProfileRequest{
		Name:       "app-missing",
		AuthMethod: "github_app",
		AppId:      0,
		PrivateKey: []byte("pem-key"),
	})
	missingAppReq.Header().Set("Cookie", "session_token="+rawCookie)
	_, err = client.CreateAuthProfile(ctx, missingAppReq)
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("missing app_id want CodeInvalidArgument, got: %v", err)
	}

	// 5. Validation: upstream validator failure passthrough
	validator.shouldFail = true
	failingReq := connect.NewRequest(&supervisorv1.CreateAuthProfileRequest{
		Name:       "failing-profile",
		AuthMethod: "pat",
		Token:      "ghp_invalid",
	})
	failingReq.Header().Set("Cookie", "session_token="+rawCookie)
	_, err = client.CreateAuthProfile(ctx, failingReq)
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("failing validator want CodeInvalidArgument, got: %v", err)
	}
	validator.shouldFail = false

	// 6. Create valid PAT profile (write-only secret)
	rawPATSecret := "ghp_superSecretToken12345"
	createPatReq := connect.NewRequest(&supervisorv1.CreateAuthProfileRequest{
		Name:       "github-pat-prod",
		AuthMethod: "pat",
		Token:      rawPATSecret,
	})
	createPatReq.Header().Set("Cookie", "session_token="+rawCookie)
	createPatRes, err := client.CreateAuthProfile(ctx, createPatReq)
	if err != nil {
		t.Fatalf("CreateAuthProfile (PAT) failed: %v", err)
	}

	patProfile := createPatRes.Msg.Profile
	if patProfile.Id <= 0 || patProfile.Name != "github-pat-prod" {
		t.Fatalf("unexpected created profile: %+v", patProfile)
	}
	if !patProfile.HasToken || patProfile.HasPrivateKey {
		t.Errorf("expected HasToken=true, HasPrivateKey=false; got HasToken=%v, HasPrivateKey=%v", patProfile.HasToken, patProfile.HasPrivateKey)
	}

	// Verify at database level: raw secret must NOT be in DB in plaintext!
	dbProfile, err := database.GetAuthProfileById(ctx, patProfile.Id)
	if err != nil {
		t.Fatalf("GetAuthProfileById failed: %v", err)
	}
	if !dbProfile.TokenEncrypted.Valid || dbProfile.TokenEncrypted.String == "" {
		t.Fatal("expected token_encrypted to be valid in DB")
	}
	if dbProfile.TokenEncrypted.String == rawPATSecret {
		t.Fatal("SECURITY VIOLATION: Database row contains plaintext token!")
	}
	// Decrypt and verify it recovers the original secret
	decryptedToken, err := db.Decrypt(dbEncKey, dbProfile.TokenEncrypted.String)
	if err != nil || decryptedToken != rawPATSecret {
		t.Fatalf("failed to decrypt stored token: %v, got %q", err, decryptedToken)
	}

	// 7. Create valid GitHub App profile
	rawAppKey := "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA..."
	createAppReq := connect.NewRequest(&supervisorv1.CreateAuthProfileRequest{
		Name:       "github-app-prod",
		AuthMethod: "github_app",
		AppId:      45678,
		PrivateKey: []byte(rawAppKey),
	})
	createAppReq.Header().Set("Cookie", "session_token="+rawCookie)
	createAppRes, err := client.CreateAuthProfile(ctx, createAppReq)
	if err != nil {
		t.Fatalf("CreateAuthProfile (App) failed: %v", err)
	}
	appProfile := createAppRes.Msg.Profile
	if !appProfile.HasPrivateKey || appProfile.HasToken {
		t.Errorf("expected HasPrivateKey=true, HasToken=false; got HasPrivateKey=%v, HasToken=%v", appProfile.HasPrivateKey, appProfile.HasToken)
	}

	// 8. ListAuthProfiles returns both profiles with boolean indicators
	listReq := connect.NewRequest(&supervisorv1.ListAuthProfilesRequest{})
	listReq.Header().Set("Cookie", "session_token="+rawCookie)
	listRes, err := client.ListAuthProfiles(ctx, listReq)
	if err != nil {
		t.Fatalf("ListAuthProfiles failed: %v", err)
	}
	if len(listRes.Msg.Profiles) != 2 {
		t.Fatalf("expected 2 profiles in list, got: %d", len(listRes.Msg.Profiles))
	}

	// 9. DeleteAuthProfile blocked when referenced by runner pool
	poolClient := supervisorv1connect.NewPoolServiceClient(ts.Client(), ts.URL)
	poolReq := connect.NewRequest(&supervisorv1.CreatePoolRequest{
		Pool: &supervisorv1.Pool{
			Name:          "ref-pool",
			Provider:      "github",
			RepositoryUrl: "https://github.com/org/repo",
			AuthProfileId: patProfile.Id,
		},
	})
	poolReq.Header().Set("Cookie", "session_token="+rawCookie)
	_, err = poolClient.CreatePool(ctx, poolReq)
	if err != nil {
		t.Fatalf("CreatePool failed: %v", err)
	}

	// Attempting to delete patProfile must fail with CodeFailedPrecondition
	delPatReq := connect.NewRequest(&supervisorv1.DeleteAuthProfileRequest{Id: patProfile.Id})
	delPatReq.Header().Set("Cookie", "session_token="+rawCookie)
	_, err = client.DeleteAuthProfile(ctx, delPatReq)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("deleting referenced auth profile want CodeFailedPrecondition, got: %v", err)
	}

	// 10. Deleting unreferenced appProfile succeeds
	delAppReq := connect.NewRequest(&supervisorv1.DeleteAuthProfileRequest{Id: appProfile.Id})
	delAppReq.Header().Set("Cookie", "session_token="+rawCookie)
	delAppRes, err := client.DeleteAuthProfile(ctx, delAppReq)
	if err != nil {
		t.Fatalf("DeleteAuthProfile failed: %v", err)
	}
	if !delAppRes.Msg.Success {
		t.Error("DeleteAuthProfile want success=true")
	}

	// Second delete returns CodeNotFound
	_, err = client.DeleteAuthProfile(ctx, delAppReq)
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("deleting non-existent profile want CodeNotFound, got: %v", err)
	}

	// Verify audit logs recorded
	auditLogs, err := database.ListAuditLogs(ctx, db.ListAuditLogsParams{Limit: 20, Offset: 0})
	if err != nil {
		t.Fatalf("ListAuditLogs failed: %v", err)
	}
	var foundCreate, foundDelete bool
	for _, l := range auditLogs {
		if l.Action == "auth_profile.create" {
			foundCreate = true
		}
		if l.Action == "auth_profile.delete" {
			foundDelete = true
		}
	}
	if !foundCreate || !foundDelete {
		t.Errorf("expected auth_profile.create and auth_profile.delete in audit logs, got: %+v", auditLogs)
	}
}

func TestAuthProfileServiceUnauthenticated(t *testing.T) {
	ctx := context.Background()
	database, jwtSecret := setupTestDB(t)

	srv := server.New(server.Options{
		Port:             8080,
		AuthDB:           database,
		AuthProfileDB:    database,
		JWTSigningSecret: jwtSecret,
	})

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	client := supervisorv1connect.NewAuthProfileServiceClient(ts.Client(), ts.URL)
	_, err := client.ListAuthProfiles(ctx, connect.NewRequest(&supervisorv1.ListAuthProfilesRequest{}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("unauthenticated ListAuthProfiles want CodeUnauthenticated, got: %v", err)
	}
}
