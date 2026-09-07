package server_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	supervisorv1 "github.com/noosxe/runnero/internal/pb/supervisor/v1"
	"github.com/noosxe/runnero/internal/pb/supervisor/v1/supervisorv1connect"
	"github.com/noosxe/runnero/internal/server"
)

func generateRSAPEMForLeakageTest(t *testing.T) string {
	t.Helper()
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate RSA key: %v", err)
	}
	privDER := x509.MarshalPKCS1PrivateKey(privKey)
	var buf bytes.Buffer
	if err := pem.Encode(&buf, &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: privDER,
	}); err != nil {
		t.Fatalf("failed to encode RSA PEM: %v", err)
	}
	return buf.String()
}

func TestRPCResponses_NoSecretLeakage(t *testing.T) {
	ctx := context.Background()
	database, jwtSecret := setupTestDB(t)

	dbEncKey := []byte("01234567890123456789012345678901") // 32 bytes
	stats := newMockStatsProvider()

	srv := server.New(server.Options{
		Port:             8080,
		AuthDB:           database,
		PoolDB:           database,
		PoolStats:        stats,
		AuthProfileDB:    database,
		DBEncryptionKey:  dbEncKey,
		OnboardingDB:     database,
		JWTSigningSecret: jwtSecret,
	})

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	authClient := supervisorv1connect.NewAuthServiceClient(ts.Client(), ts.URL)
	poolClient := supervisorv1connect.NewPoolServiceClient(ts.Client(), ts.URL)
	profileClient := supervisorv1connect.NewAuthProfileServiceClient(ts.Client(), ts.URL)
	onboardingClient := supervisorv1connect.NewOnboardingServiceClient(ts.Client(), ts.URL)

	adminPassword := "super-secret-admin-pass-999!"
	rawPEMKey := generateRSAPEMForLeakageTest(t)
	classicPAT := "ghp_TopSecretClassicPAT999999999999"
	finePAT := "github_pat_11SECRET_FINE_GRAINED_TOKEN_abcdef1234567890"
	giteaToken := "gitea_secret_token_11223344556677889900"
	forgejoToken := "forgejo_secret_token_99887766554433221100"

	// 1. Setup Admin
	setupRes, err := authClient.SetupAdmin(ctx, connect.NewRequest(&supervisorv1.SetupAdminRequest{
		Username: "security-admin",
		Password: adminPassword,
	}))
	if err != nil {
		t.Fatalf("SetupAdmin failed: %v", err)
	}

	// 2. Login
	loginRes, err := authClient.Login(ctx, connect.NewRequest(&supervisorv1.LoginRequest{
		Username: "security-admin",
		Password: adminPassword,
	}))
	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}
	cookieHeader := loginRes.Header().Get("Set-Cookie")
	sessionToken := strings.Split(strings.Split(cookieHeader, ";")[0], "=")[1]

	// 3. GetSession
	getSessRes, err := authClient.GetSession(ctx, withAuth(sessionToken, &supervisorv1.GetSessionRequest{}))
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}

	// 4. Create multiple auth profiles with sensitive secrets
	createAppRes, err := profileClient.CreateAuthProfile(ctx, withAuth(sessionToken, &supervisorv1.CreateAuthProfileRequest{
		Name:       "gh-app-secret",
		AuthMethod: "github_app",
		AppId:      5555,
		PrivateKey: []byte(rawPEMKey),
	}))
	if err != nil {
		t.Fatalf("CreateAuthProfile (github_app) failed: %v", err)
	}

	createClassicRes, err := profileClient.CreateAuthProfile(ctx, withAuth(sessionToken, &supervisorv1.CreateAuthProfileRequest{
		Name:       "gh-pat-secret",
		AuthMethod: "pat",
		Token:      classicPAT,
	}))
	if err != nil {
		t.Fatalf("CreateAuthProfile (classic PAT) failed: %v", err)
	}

	createFineRes, err := profileClient.CreateAuthProfile(ctx, withAuth(sessionToken, &supervisorv1.CreateAuthProfileRequest{
		Name:       "gh-fine-secret",
		AuthMethod: "pat",
		Token:      finePAT,
	}))
	if err != nil {
		t.Fatalf("CreateAuthProfile (fine-grained PAT) failed: %v", err)
	}

	createGiteaRes, err := profileClient.CreateAuthProfile(ctx, withAuth(sessionToken, &supervisorv1.CreateAuthProfileRequest{
		Name:       "gitea-secret",
		AuthMethod: "gitea_token",
		Token:      giteaToken,
	}))
	if err != nil {
		t.Fatalf("CreateAuthProfile (Gitea) failed: %v", err)
	}

	createForgejoRes, err := profileClient.CreateAuthProfile(ctx, withAuth(sessionToken, &supervisorv1.CreateAuthProfileRequest{
		Name:       "forgejo-secret",
		AuthMethod: "forgejo_token",
		Token:      forgejoToken,
	}))
	if err != nil {
		t.Fatalf("CreateAuthProfile (Forgejo) failed: %v", err)
	}

	// 5. ListAuthProfiles
	listProfilesRes, err := profileClient.ListAuthProfiles(ctx, withAuth(sessionToken, &supervisorv1.ListAuthProfilesRequest{}))
	if err != nil {
		t.Fatalf("ListAuthProfiles failed: %v", err)
	}

	// 6. Create Pool & Update Pool
	createPoolRes, err := poolClient.CreatePool(ctx, withAuth(sessionToken, &supervisorv1.CreatePoolRequest{
		Pool: &supervisorv1.Pool{
			Name:           "secure-pool",
			Provider:       "github",
			RepositoryUrl:  "https://github.com/myorg/repo",
			Scope:          "repo",
			RunnerImage:    "ghcr.io/noosxe/runnero:latest",
			MaxConcurrency: 5,
			MinIdleRunners: 1,
			Labels:         []string{"self-hosted"},
			AuthProfileId:  createClassicRes.Msg.Profile.Id,
		},
	}))
	if err != nil {
		t.Fatalf("CreatePool failed: %v", err)
	}

	updatePoolRes, err := poolClient.UpdatePool(ctx, withAuth(sessionToken, &supervisorv1.UpdatePoolRequest{
		Pool: &supervisorv1.Pool{
			Id:             createPoolRes.Msg.Pool.Id,
			Name:           "secure-pool",
			Provider:       "github",
			RepositoryUrl:  "https://github.com/myorg/repo",
			Scope:          "repo",
			RunnerImage:    "ghcr.io/noosxe/runnero:v2",
			MaxConcurrency: 10,
			MinIdleRunners: 2,
			Labels:         []string{"self-hosted", "arm64"},
			AuthProfileId:  createClassicRes.Msg.Profile.Id,
		},
	}))
	if err != nil {
		t.Fatalf("UpdatePool failed: %v", err)
	}

	listPoolsRes, err := poolClient.ListPools(ctx, withAuth(sessionToken, &supervisorv1.ListPoolsRequest{}))
	if err != nil {
		t.Fatalf("ListPools failed: %v", err)
	}

	// 7. Onboarding & App Settings
	onboardingStatusRes, err := onboardingClient.GetOnboardingStatus(ctx, withAuth(sessionToken, &supervisorv1.GetOnboardingStatusRequest{}))
	if err != nil {
		t.Fatalf("GetOnboardingStatus failed: %v", err)
	}

	appSettingsRes, err := onboardingClient.GetAppSettings(ctx, withAuth(sessionToken, &supervisorv1.GetAppSettingsRequest{}))
	if err != nil {
		t.Fatalf("GetAppSettings failed: %v", err)
	}

	// 8. Collect all response proto messages
	protoResponses := []proto.Message{
		setupRes.Msg,
		loginRes.Msg,
		getSessRes.Msg,
		createAppRes.Msg,
		createClassicRes.Msg,
		createFineRes.Msg,
		createGiteaRes.Msg,
		createForgejoRes.Msg,
		listProfilesRes.Msg,
		createPoolRes.Msg,
		updatePoolRes.Msg,
		listPoolsRes.Msg,
		onboardingStatusRes.Msg,
		appSettingsRes.Msg,
	}

	// Also extract admin password hash & session token hash from DB to assert they are not leaked
	dbUser, err := database.GetAdminUserByUsername(ctx, "security-admin")
	if err != nil {
		t.Fatalf("GetAdminUserByUsername failed: %v", err)
	}
	dbSession, err := database.GetSessionByTokenHash(ctx, server.HashToken(sessionToken))
	if err != nil {
		t.Fatalf("GetSessionByTokenHash failed: %v", err)
	}

	// Sensitive substrings that must NEVER appear in any RPC response
	forbiddenSecrets := map[string]string{
		"Admin Plaintext Password": adminPassword,
		"Admin Password Hash":      dbUser.PasswordHash,
		"Session Token Hash":       dbSession.TokenHash,
		"Raw RSA PEM":              rawPEMKey,
		"RSA PEM Header":           "-----BEGIN RSA PRIVATE KEY-----",
		"Classic PAT":              classicPAT,
		"Fine-Grained PAT":         finePAT,
		"Gitea Secret Token":       giteaToken,
		"Forgejo Secret Token":     forgejoToken,
		"DB Encryption Key":        string(dbEncKey),
		"JWT Secret":               string(jwtSecret),
	}

	for idx, msg := range protoResponses {
		msgName := string(msg.ProtoReflect().Descriptor().FullName())

		// Test JSON representation
		jsonBytes, err := protojson.Marshal(msg)
		if err != nil {
			t.Fatalf("[%d: %s] protojson.Marshal failed: %v", idx, msgName, err)
		}
		jsonStr := string(jsonBytes)

		// Test Binary wire representation
		wireBytes, err := proto.Marshal(msg)
		if err != nil {
			t.Fatalf("[%d: %s] proto.Marshal failed: %v", idx, msgName, err)
		}
		wireStr := string(wireBytes)

		for secretLabel, secretValue := range forbiddenSecrets {
			if strings.Contains(jsonStr, secretValue) {
				t.Fatalf("CRITICAL SECURITY LEAKAGE in RPC response [%s] (JSON): leaked %s: %q", msgName, secretLabel, secretValue)
			}
			if strings.Contains(wireStr, secretValue) {
				t.Fatalf("CRITICAL SECURITY LEAKAGE in RPC response [%s] (Binary wire): leaked %s: %q", msgName, secretLabel, secretValue)
			}
		}
	}

	// 9. Positive verification on AuthProfile: HasPrivateKey and HasToken booleans
	for _, prof := range listProfilesRes.Msg.Profiles {
		switch prof.Name {
		case "gh-app-secret":
			if !prof.HasPrivateKey {
				t.Errorf("profile gh-app-secret want HasPrivateKey=true, got false")
			}
			if prof.HasToken {
				t.Errorf("profile gh-app-secret want HasToken=false, got true")
			}
		case "gh-pat-secret", "gh-fine-secret", "gitea-secret", "forgejo-secret":
			if prof.HasPrivateKey {
				t.Errorf("profile %s want HasPrivateKey=false, got true", prof.Name)
			}
			if !prof.HasToken {
				t.Errorf("profile %s want HasToken=true, got false", prof.Name)
			}
		}
	}
}
