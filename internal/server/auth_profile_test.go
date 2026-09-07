package server_test

import (
	"context"
	"database/sql"
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
	calls      int
}

func (m *mockValidator) ValidateCredentials(_ context.Context, req *supervisorv1.CreateAuthProfileRequest) error {
	m.calls++
	if m.shouldFail {
		return errors.New("upstream git provider rejected token")
	}
	return nil
}

// newAuthedProfileClient boots a supervisor server against a fresh in-memory
// database, registers an admin, logs in, and returns an authed AuthProfile
// service client plus the raw session cookie and the underlying database.
func newAuthedProfileClient(t *testing.T, validator server.CredentialValidator) (supervisorv1connect.AuthProfileServiceClient, string, *db.DB) {
	t.Helper()
	ctx := context.Background()
	database, jwtSecret := setupTestDB(t)

	srv := server.New(server.Options{
		Port:                8080,
		AuthDB:              database,
		PoolDB:              database,
		AuthProfileDB:       database,
		DBEncryptionKey:     []byte("01234567890123456789012345678901"), // 32 bytes
		CredentialValidator: validator,
		JWTSigningSecret:    jwtSecret,
	})

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	authClient := supervisorv1connect.NewAuthServiceClient(ts.Client(), ts.URL)
	if _, err := authClient.SetupAdmin(ctx, connect.NewRequest(&supervisorv1.SetupAdminRequest{
		Username: "admin",
		Password: "password123",
	})); err != nil {
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

	return supervisorv1connect.NewAuthProfileServiceClient(ts.Client(), ts.URL), rawCookie, database
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

// TestAuthProfileServiceUpdate covers the UpdateAuthProfile RPC (docs/17):
// rename, blank-secret ciphertext preservation, token rotation, duplicate-name
// mapping, method-switch purge semantics, degenerate-row self-heal, and the
// validator-invoked-only-on-rotation rule.
func TestAuthProfileServiceUpdate(t *testing.T) {
	ctx := context.Background()
	validator := &mockValidator{}
	client, rawCookie, database := newAuthedProfileClient(t, validator)

	update := func(id int64, name, method string, appID int64, privKey []byte, token string) (*supervisorv1.AuthProfile, error) {
		req := connect.NewRequest(&supervisorv1.UpdateAuthProfileRequest{
			Id:         id,
			Name:       name,
			AuthMethod: method,
			AppId:      appID,
			PrivateKey: privKey,
			Token:      token,
		})
		req.Header().Set("Cookie", "session_token="+rawCookie)
		res, err := client.UpdateAuthProfile(ctx, req)
		if err != nil {
			return nil, err
		}
		return res.Msg.Profile, nil
	}

	const (
		initialToken = "ghp_originalSecretToken"
		rotatedToken = "ghp_rotatedSecretToken"
	)

	// Seed: two PAT profiles and one GitHub App profile.
	pat, err := client.CreateAuthProfile(ctx, withAuthProfile(rawCookie, &supervisorv1.CreateAuthProfileRequest{
		Name: "pat-edit-target", AuthMethod: "pat", Token: initialToken,
	}))
	if err != nil {
		t.Fatalf("seed PAT profile: %v", err)
	}
	other, err := client.CreateAuthProfile(ctx, withAuthProfile(rawCookie, &supervisorv1.CreateAuthProfileRequest{
		Name: "pat-other", AuthMethod: "pat", Token: "ghp_otherToken",
	}))
	if err != nil {
		t.Fatalf("seed second PAT profile: %v", err)
	}
	app, err := client.CreateAuthProfile(ctx, withAuthProfile(rawCookie, &supervisorv1.CreateAuthProfileRequest{
		Name: "app-edit-target", AuthMethod: "github_app", AppId: 111, PrivateKey: []byte("k1"),
	}))
	if err != nil {
		t.Fatalf("seed GitHub App profile: %v", err)
	}

	cipherBefore, err := database.GetAuthProfileById(ctx, pat.Msg.Profile.Id)
	if err != nil {
		t.Fatalf("fetch seeded PAT: %v", err)
	}

	validator.calls = 0

	// 1. Rename-only: blank token keeps the stored ciphertext; validator must
	// NOT run (no new secret supplied).
	renamed, err := update(pat.Msg.Profile.Id, "pat-renamed", "pat", 0, nil, "")
	if err != nil {
		t.Fatalf("rename-only update failed: %v", err)
	}
	if renamed.Name != "pat-renamed" || !renamed.HasToken || renamed.HasPrivateKey {
		t.Errorf("unexpected renamed profile: %+v", renamed)
	}
	if validator.calls != 0 {
		t.Errorf("validator invoked %d times on rename-only update, want 0", validator.calls)
	}
	cipherAfter, err := database.GetAuthProfileById(ctx, pat.Msg.Profile.Id)
	if err != nil {
		t.Fatalf("refetch renamed PAT: %v", err)
	}
	if cipherAfter.TokenEncrypted.String != cipherBefore.TokenEncrypted.String {
		t.Error("SECURITY: blank-token update replaced the stored ciphertext")
	}
	if got := decryptAtRest(t, cipherAfter.TokenEncrypted.String); got != initialToken {
		t.Errorf("kept ciphertext decrypts to %q, want the original token", got)
	}

	// 2. Token rotation: ciphertext changes and decrypts to the new secret;
	// validator runs exactly once.
	rotated, err := update(pat.Msg.Profile.Id, "pat-renamed", "pat", 0, nil, rotatedToken)
	if err != nil {
		t.Fatalf("token rotation failed: %v", err)
	}
	if !rotated.HasToken {
		t.Error("rotated profile want HasToken=true")
	}
	if validator.calls != 1 {
		t.Errorf("validator invoked %d times on rotation, want 1", validator.calls)
	}
	rotatedRow, err := database.GetAuthProfileById(ctx, pat.Msg.Profile.Id)
	if err != nil {
		t.Fatalf("refetch rotated PAT: %v", err)
	}
	if rotatedRow.TokenEncrypted.String == cipherBefore.TokenEncrypted.String {
		t.Error("token rotation stored the old ciphertext")
	}
	if got := decryptAtRest(t, rotatedRow.TokenEncrypted.String); got != rotatedToken {
		t.Errorf("stored token decrypts to %q, want %q", got, rotatedToken)
	}

	// 3. Renaming onto an existing profile name maps to CodeAlreadyExists.
	_, err = update(other.Msg.Profile.Id, "pat-renamed", "pat", 0, nil, "")
	if connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("duplicate name want CodeAlreadyExists, got: %v", err)
	}

	// 4. Unknown id maps to CodeNotFound.
	if _, err = update(99999, "nope", "pat", 0, nil, ""); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown id want CodeNotFound, got: %v", err)
	}

	// 5. Static payload validation maps to CodeInvalidArgument.
	for _, tc := range []struct {
		name                string
		id                  int64
		profileName, method string
		appID               int64
	}{
		{"zero id", 0, "x", "pat", 0},
		{"empty name", pat.Msg.Profile.Id, "   ", "pat", 0},
		{"unsupported method", pat.Msg.Profile.Id, "x", "oauth1", 0},
		{"github_app without app_id", pat.Msg.Profile.Id, "x", "github_app", 0},
	} {
		if _, err = update(tc.id, tc.profileName, tc.method, tc.appID, nil, "t"); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s want CodeInvalidArgument, got: %v", tc.name, err)
		}
	}

	// 6. Method switch github_app -> pat without a new token is rejected.
	if _, err = update(app.Msg.Profile.Id, "app-edit-target", "pat", 0, nil, ""); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("method switch without secret want CodeInvalidArgument, got: %v", err)
	}

	// 7. Method switch with the new secret purges the old method's material.
	switched, err := update(app.Msg.Profile.Id, "app-switched-to-pat", "pat", 0, nil, "ghp_switchToken")
	if err != nil {
		t.Fatalf("method switch failed: %v", err)
	}
	if switched.HasPrivateKey || switched.AppId != 0 {
		t.Errorf("switched profile want HasPrivateKey=false and AppId=0, got %+v", switched)
	}
	switchedRow, err := database.GetAuthProfileById(ctx, app.Msg.Profile.Id)
	if err != nil {
		t.Fatalf("refetch switched profile: %v", err)
	}
	if switchedRow.PrivateKeyEncrypted.Valid && switchedRow.PrivateKeyEncrypted.String != "" {
		t.Error("SECURITY: old private_key material survived the method switch")
	}
	if switchedRow.AppID.Valid {
		t.Error("app_id survived the switch to a token method")
	}
	if got := decryptAtRest(t, switchedRow.TokenEncrypted.String); got != "ghp_switchToken" {
		t.Errorf("switched token decrypts to %q", got)
	}

	// 8. Degenerate row (token method without stored token): blank secret
	// yields CodeFailedPrecondition; supplying one self-heals the row.
	degenerate, err := database.CreateAuthProfile(ctx, db.CreateAuthProfileParams{
		Name:           "degenerate-pat",
		AuthMethod:     "pat",
		TokenEncrypted: sql.NullString{},
	})
	if err != nil {
		t.Fatalf("seed degenerate profile: %v", err)
	}
	if _, err = update(degenerate.ID, "degenerate-pat", "pat", 0, nil, ""); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("degenerate row blank token want CodeFailedPrecondition, got: %v", err)
	}
	if _, err = update(degenerate.ID, "degenerate-pat", "pat", 0, nil, "ghp_healToken"); err != nil {
		t.Fatalf("degenerate row self-heal failed: %v", err)
	}

	// 9. Audit trail: auth_profile.update entries with before/after metadata.
	auditLogs, err := database.ListAuditLogs(ctx, db.ListAuditLogsParams{Limit: 50, Offset: 0})
	if err != nil {
		t.Fatalf("ListAuditLogs failed: %v", err)
	}
	foundUpdate := false
	for _, l := range auditLogs {
		if l.Action == "auth_profile.update" {
			foundUpdate = true
		}
	}
	if !foundUpdate {
		t.Error("expected auth_profile.update in audit logs")
	}
}

// withAuthProfile attaches the session cookie to an auth profile RPC request.
func withAuthProfile(sessionToken string, msg *supervisorv1.CreateAuthProfileRequest) *connect.Request[supervisorv1.CreateAuthProfileRequest] {
	req := connect.NewRequest(msg)
	req.Header().Set("Cookie", "session_token="+sessionToken)
	return req
}

// testDBEncryptionKey matches the key configured in newAuthedProfileClient.
const testDBEncryptionKey = "01234567890123456789012345678901" // 32 bytes

// decryptAtRest decrypts a stored ciphertext with the test encryption key,
// failing the test instead of leaking an error path.
func decryptAtRest(t *testing.T, ciphertext string) string {
	t.Helper()
	plaintext, err := db.Decrypt([]byte(testDBEncryptionKey), ciphertext)
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}
	return plaintext
}
