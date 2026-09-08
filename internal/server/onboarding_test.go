package server_test

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/noosxe/runnero/internal/db"
	supervisorv1 "github.com/noosxe/runnero/internal/pb/supervisor/v1"
	"github.com/noosxe/runnero/internal/pb/supervisor/v1/supervisorv1connect"
	"github.com/noosxe/runnero/internal/server"
)

func TestOnboardingStatusLifecycle(t *testing.T) {
	ctx := context.Background()
	database, jwtSecret := setupTestDB(t)

	srv := server.New(server.Options{
		Port:             8080,
		AuthDB:           database,
		PoolDB:           database,
		AuthProfileDB:    database,
		OnboardingDB:     database,
		JWTSigningSecret: jwtSecret,
	})

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	client := supervisorv1connect.NewOnboardingServiceClient(ts.Client(), ts.URL)

	// 1. Initial state: nothing created
	statusRes, err := client.GetOnboardingStatus(ctx, connect.NewRequest(&supervisorv1.GetOnboardingStatusRequest{}))
	if err != nil {
		t.Fatalf("GetOnboardingStatus failed: %v", err)
	}
	if statusRes.Msg.AdminCreated || statusRes.Msg.AuthProfileExists || statusRes.Msg.PoolExists || statusRes.Msg.SetupComplete {
		t.Fatalf("expected all false, got: %+v", statusRes.Msg)
	}
	if statusRes.Msg.HostArch != server.HostArch() || statusRes.Msg.HostOs != server.HostOS() {
		t.Fatalf("expected host arch/os %s/%s, got %s/%s", server.HostArch(), server.HostOS(), statusRes.Msg.HostArch, statusRes.Msg.HostOs)
	}

	// 2. Create admin
	authClient := supervisorv1connect.NewAuthServiceClient(ts.Client(), ts.URL)
	_, err = authClient.SetupAdmin(ctx, connect.NewRequest(&supervisorv1.SetupAdminRequest{
		Username: "admin",
		Password: "password123",
	}))
	if err != nil {
		t.Fatalf("SetupAdmin failed: %v", err)
	}

	statusRes, err = client.GetOnboardingStatus(ctx, connect.NewRequest(&supervisorv1.GetOnboardingStatusRequest{}))
	if err != nil {
		t.Fatalf("GetOnboardingStatus after admin failed: %v", err)
	}
	if !statusRes.Msg.AdminCreated || statusRes.Msg.AuthProfileExists || statusRes.Msg.PoolExists || statusRes.Msg.SetupComplete {
		t.Fatalf("expected only AdminCreated=true, got: %+v", statusRes.Msg)
	}

	// 3. Create auth profile
	prof, err := database.CreateAuthProfile(ctx, db.CreateAuthProfileParams{
		Name:           "test-profile",
		AuthMethod:     "pat",
		TokenEncrypted: sql.NullString{String: "enc", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateAuthProfile failed: %v", err)
	}

	statusRes, err = client.GetOnboardingStatus(ctx, connect.NewRequest(&supervisorv1.GetOnboardingStatusRequest{}))
	if err != nil {
		t.Fatalf("GetOnboardingStatus after profile failed: %v", err)
	}
	if !statusRes.Msg.AdminCreated || !statusRes.Msg.AuthProfileExists || statusRes.Msg.PoolExists || statusRes.Msg.SetupComplete {
		t.Fatalf("expected AdminCreated and AuthProfileExists true, got: %+v", statusRes.Msg)
	}

	// 4. Create pool
	_, err = database.CreateRunnerPool(ctx, db.CreateRunnerPoolParams{
		Name:          "test-pool",
		Provider:      "github",
		RepositoryUrl: "https://github.com/org/repo",
		Scope:         "repo",
		AuthProfileID: prof.ID,
		Labels:        "linux",
		RunnerImage:   "img",
	})
	if err != nil {
		t.Fatalf("CreateRunnerPool failed: %v", err)
	}

	statusRes, err = client.GetOnboardingStatus(ctx, connect.NewRequest(&supervisorv1.GetOnboardingStatusRequest{}))
	if err != nil {
		t.Fatalf("GetOnboardingStatus after pool failed: %v", err)
	}
	if !statusRes.Msg.AdminCreated || !statusRes.Msg.AuthProfileExists || !statusRes.Msg.PoolExists || !statusRes.Msg.SetupComplete {
		t.Fatalf("expected all true (SetupComplete=true), got: %+v", statusRes.Msg)
	}
}

func TestAppSettingsGetAndSet(t *testing.T) {
	ctx := context.Background()
	database, jwtSecret := setupTestDB(t)

	srv := server.New(server.Options{
		Port:             8080,
		AuthDB:           database,
		OnboardingDB:     database,
		JWTSigningSecret: jwtSecret,
	})

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Authenticate first
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

	client := supervisorv1connect.NewOnboardingServiceClient(ts.Client(), ts.URL)

	// 1. Get seeded settings
	getReq := connect.NewRequest(&supervisorv1.GetAppSettingsRequest{})
	getReq.Header().Set("Cookie", "session_token="+rawCookie)
	getRes, err := client.GetAppSettings(ctx, getReq)
	if err != nil {
		t.Fatalf("GetAppSettings failed: %v", err)
	}
	if len(getRes.Msg.Settings) == 0 {
		t.Fatal("expected seeded app settings")
	}

	// 2. SetAppSetting updates a key
	setReq := connect.NewRequest(&supervisorv1.SetAppSettingRequest{
		Key:   "total_allowed_runners",
		Value: "25",
	})
	setReq.Header().Set("Cookie", "session_token="+rawCookie)
	setRes, err := client.SetAppSetting(ctx, setReq)
	if err != nil {
		t.Fatalf("SetAppSetting failed: %v", err)
	}
	if setRes.Msg.Key != "total_allowed_runners" || setRes.Msg.Value != "25" {
		t.Errorf("unexpected set response: %+v", setRes.Msg)
	}

	// 3. Verify in DB and via GetAppSettings
	getRes2, err := client.GetAppSettings(ctx, getReq)
	if err != nil {
		t.Fatalf("GetAppSettings second call failed: %v", err)
	}
	var found bool
	for _, s := range getRes2.Msg.Settings {
		if s.Key == "total_allowed_runners" && s.Value == "25" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected updated setting total_allowed_runners=25, got: %+v", getRes2.Msg.Settings)
	}
}

func TestCompleteOnboarding(t *testing.T) {
	ctx := context.Background()
	database, jwtSecret := setupTestDB(t)

	srv := server.New(server.Options{
		Port:             8080,
		AuthDB:           database,
		OnboardingDB:     database,
		JWTSigningSecret: jwtSecret,
	})

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	client := supervisorv1connect.NewOnboardingServiceClient(ts.Client(), ts.URL)
	authClient := supervisorv1connect.NewAuthServiceClient(ts.Client(), ts.URL)

	// 1. Calling CompleteOnboarding before admin exists or without auth should fail
	_, err := client.CompleteOnboarding(ctx, connect.NewRequest(&supervisorv1.CompleteOnboardingRequest{}))
	if err == nil {
		t.Fatal("expected CompleteOnboarding without admin/auth to fail")
	}

	// 2. Setup admin
	_, err = authClient.SetupAdmin(ctx, connect.NewRequest(&supervisorv1.SetupAdminRequest{
		Username: "admin",
		Password: "password123",
	}))
	if err != nil {
		t.Fatalf("SetupAdmin failed: %v", err)
	}

	// Log in to get session cookie
	loginRes, err := authClient.Login(ctx, connect.NewRequest(&supervisorv1.LoginRequest{
		Username: "admin",
		Password: "password123",
	}))
	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}
	cookie := loginRes.Header().Get("Set-Cookie")
	rawCookie := strings.Split(strings.Split(cookie, ";")[0], "=")[1]

	// 3. Before complete, setup is not complete
	statusRes, err := client.GetOnboardingStatus(ctx, connect.NewRequest(&supervisorv1.GetOnboardingStatusRequest{}))
	if err != nil {
		t.Fatalf("GetOnboardingStatus failed: %v", err)
	}
	if !statusRes.Msg.AdminCreated || statusRes.Msg.SetupComplete || statusRes.Msg.OnboardingCompleted {
		t.Fatalf("expected AdminCreated=true, SetupComplete=false, OnboardingCompleted=false, got: %+v", statusRes.Msg)
	}

	// 4. Complete onboarding
	compReq := connect.NewRequest(&supervisorv1.CompleteOnboardingRequest{})
	compReq.Header().Set("Cookie", "session_token="+rawCookie)
	compRes, err := client.CompleteOnboarding(ctx, compReq)
	if err != nil {
		t.Fatalf("CompleteOnboarding failed: %v", err)
	}
	if !compRes.Msg.Success {
		t.Fatalf("expected Success=true, got: %+v", compRes.Msg)
	}

	// 5. Verify status now reports setup complete and onboarding completed
	statusRes, err = client.GetOnboardingStatus(ctx, connect.NewRequest(&supervisorv1.GetOnboardingStatusRequest{}))
	if err != nil {
		t.Fatalf("GetOnboardingStatus failed: %v", err)
	}
	if !statusRes.Msg.AdminCreated || !statusRes.Msg.SetupComplete || !statusRes.Msg.OnboardingCompleted {
		t.Fatalf("expected AdminCreated=true, SetupComplete=true, OnboardingCompleted=true, got: %+v", statusRes.Msg)
	}

	// 6. Verify audit log entry
	logs, err := database.ListAuditLogs(ctx, db.ListAuditLogsParams{
		Limit:  10,
		Offset: 0,
	})
	if err != nil {
		t.Fatalf("ListAuditLogs failed: %v", err)
	}
	var auditFound bool
	for _, l := range logs {
		if l.Action == "onboarding.complete" && l.ResourceType.Valid && l.ResourceType.String == "onboarding" {
			auditFound = true
			break
		}
	}
	if !auditFound {
		t.Fatalf("expected audit log for onboarding.complete, got: %+v", logs)
	}
}
