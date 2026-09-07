package server_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/noosxe/runnero/internal/db"
	supervisorv1 "github.com/noosxe/runnero/internal/pb/supervisor/v1"
	"github.com/noosxe/runnero/internal/pb/supervisor/v1/supervisorv1connect"
	"github.com/noosxe/runnero/internal/server"
)

type mockTerminator struct {
	terminated []string
}

func (m *mockTerminator) PoolRunners(poolName string) []server.RunnerInstanceInfo {
	return []server.RunnerInstanceInfo{
		{
			ID:        "c-12345",
			Name:      "test-runner-1",
			PoolName:  poolName,
			State:     "running",
			IPAddress: "172.17.0.2",
			SpawnedAt: time.Now().Add(-10 * time.Minute),
			IsBusy:    false,
		},
	}
}

func (m *mockTerminator) TerminateRunner(_ context.Context, poolName, containerID string) error {
	m.terminated = append(m.terminated, poolName+":"+containerID)
	return nil
}

type mockAuditRenovateExecutor struct{}

func (m *mockAuditRenovateExecutor) Execute(_ context.Context, poolID int64) (*db.RenovateRun, error) {
	return &db.RenovateRun{
		ID:        99,
		PoolID:    poolID,
		Status:    "running",
		StartedAt: time.Now().UTC(),
	}, nil
}

func (m *mockAuditRenovateExecutor) HandleContainerExit(_ context.Context, _ string, _ int, _ string) (bool, error) {
	return false, nil
}

type mockAuditPuller struct{}

func (m *mockAuditPuller) PullImage(_ context.Context, _ string) error {
	return nil
}

func withAuth[T any](token string, msg *T) *connect.Request[T] {
	req := connect.NewRequest(msg)
	req.Header().Set("Cookie", "session_token="+token)
	return req
}

func TestAuditLogCoverage_MutatingActions(t *testing.T) {
	ctx := context.Background()
	database, jwtSecret := setupTestDB(t)

	stats := newMockStatsProvider()
	terminator := &mockTerminator{}
	renovateExec := &mockAuditRenovateExecutor{}
	puller := &mockAuditPuller{}

	srv := server.New(server.Options{
		Port:             8080,
		AuthDB:           database,
		PoolDB:           database,
		PoolStats:        stats,
		RunnerMgr:        terminator,
		AuthProfileDB:    database,
		DBEncryptionKey:  []byte("01234567890123456789012345678901"),
		ImagePuller:      puller,
		OnboardingDB:     database,
		RenovateDB:       database,
		RenovateExecutor: renovateExec,
		JWTSigningSecret: jwtSecret,
	})

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	authClient := supervisorv1connect.NewAuthServiceClient(ts.Client(), ts.URL)
	poolClient := supervisorv1connect.NewPoolServiceClient(ts.Client(), ts.URL)
	profileClient := supervisorv1connect.NewAuthProfileServiceClient(ts.Client(), ts.URL)
	onboardingClient := supervisorv1connect.NewOnboardingServiceClient(ts.Client(), ts.URL)
	imageClient := supervisorv1connect.NewImageUpdateServiceClient(ts.Client(), ts.URL)
	renovateClient := supervisorv1connect.NewRenovateServiceClient(ts.Client(), ts.URL)

	// 1. SetupAdmin -> audit action: auth.setup_admin
	setupRes, err := authClient.SetupAdmin(ctx, connect.NewRequest(&supervisorv1.SetupAdminRequest{
		Username: "admin",
		Password: "strongpassword123!",
	}))
	if err != nil || !setupRes.Msg.Success {
		t.Fatalf("SetupAdmin failed: %v", err)
	}

	// 2. Failed Login -> audit action: auth.login_failed
	_, err = authClient.Login(ctx, connect.NewRequest(&supervisorv1.LoginRequest{
		Username: "admin",
		Password: "wrongpassword",
	}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("Login want CodeUnauthenticated, got: %v", err)
	}

	// 3. Successful Login -> audit action: auth.login
	loginRes, err := authClient.Login(ctx, connect.NewRequest(&supervisorv1.LoginRequest{
		Username: "admin",
		Password: "strongpassword123!",
	}))
	if err != nil || !loginRes.Msg.Success {
		t.Fatalf("Login failed: %v", err)
	}
	cookieHeader := loginRes.Header().Get("Set-Cookie")
	sessionToken := strings.Split(strings.Split(cookieHeader, ";")[0], "=")[1]

	// 4. CreateAuthProfile -> audit action: auth_profile.create
	createProfileRes, err := profileClient.CreateAuthProfile(ctx, withAuth(sessionToken, &supervisorv1.CreateAuthProfileRequest{
		Name:       "github-pat-profile",
		AuthMethod: "pat",
		Token:      "ghp_1234567890abcdef1234567890abcdef1234",
	}))
	if err != nil {
		t.Fatalf("CreateAuthProfile failed: %v", err)
	}
	profileID := createProfileRes.Msg.Profile.Id

	// 5. CreatePool -> audit action: pool.create
	createPoolRes, err := poolClient.CreatePool(ctx, withAuth(sessionToken, &supervisorv1.CreatePoolRequest{
		Pool: &supervisorv1.Pool{
			Name:           "prod-runners",
			Provider:       "github",
			RepositoryUrl:  "https://github.com/owner/repo",
			Scope:          "repo",
			RunnerImage:    "ghcr.io/actions/runner:latest",
			MaxConcurrency: 5,
			MinIdleRunners: 1,
			Labels:         []string{"self-hosted", "linux"},
			AuthProfileId:  profileID,
		},
	}))
	if err != nil {
		t.Fatalf("CreatePool failed: %v", err)
	}
	poolID := createPoolRes.Msg.Pool.Id

	// 6. UpdatePool -> audit action: pool.update
	_, err = poolClient.UpdatePool(ctx, withAuth(sessionToken, &supervisorv1.UpdatePoolRequest{
		Pool: &supervisorv1.Pool{
			Id:             poolID,
			Name:           "prod-runners",
			Provider:       "github",
			RepositoryUrl:  "https://github.com/owner/repo",
			Scope:          "repo",
			RunnerImage:    "ghcr.io/actions/runner:v2",
			MaxConcurrency: 10,
			MinIdleRunners: 2,
			Labels:         []string{"self-hosted", "linux"},
			AuthProfileId:  profileID,
		},
	}))
	if err != nil {
		t.Fatalf("UpdatePool failed: %v", err)
	}

	// 7. TerminateRunner -> audit action: runner.terminate
	_, err = poolClient.TerminateRunner(ctx, withAuth(sessionToken, &supervisorv1.TerminateRunnerRequest{
		PoolId:      poolID,
		ContainerId: "c-12345",
	}))
	if err != nil {
		t.Fatalf("TerminateRunner failed: %v", err)
	}

	// 8. SetAppSetting -> audit action: setting.update
	_, err = onboardingClient.SetAppSetting(ctx, withAuth(sessionToken, &supervisorv1.SetAppSettingRequest{
		Key:   "analytics_enabled",
		Value: "true",
	}))
	if err != nil {
		t.Fatalf("SetAppSetting failed: %v", err)
	}

	// 9. PullImage -> audit action: image.pull
	_, err = imageClient.PullImage(ctx, withAuth(sessionToken, &supervisorv1.PullImageRequest{
		PoolId: poolID,
	}))
	if err != nil {
		t.Fatalf("PullImage failed: %v", err)
	}

	// 10. Flag & DismissImageUpdate -> audit action: image.dismiss_update
	up := srv.ImageUpdateService().FlagUpdate(poolID, "ghcr.io/actions/runner:v2", "sha256:testdigest123")

	_, err = imageClient.DismissImageUpdate(ctx, withAuth(sessionToken, &supervisorv1.DismissImageUpdateRequest{
		Id: up.Id,
	}))
	if err != nil {
		t.Fatalf("DismissImageUpdate failed: %v", err)
	}

	// 11. TriggerRenovateRun -> audit action: renovate.trigger
	_, err = renovateClient.TriggerRenovateRun(ctx, withAuth(sessionToken, &supervisorv1.TriggerRenovateRunRequest{
		PoolId: poolID,
	}))
	if err != nil {
		t.Fatalf("TriggerRenovateRun failed: %v", err)
	}

	// 12. DeletePool -> audit action: pool.delete
	_, err = poolClient.DeletePool(ctx, withAuth(sessionToken, &supervisorv1.DeletePoolRequest{
		Id: poolID,
	}))
	if err != nil {
		t.Fatalf("DeletePool failed: %v", err)
	}

	// 13. DeleteAuthProfile -> audit action: auth_profile.delete
	_, err = profileClient.DeleteAuthProfile(ctx, withAuth(sessionToken, &supervisorv1.DeleteAuthProfileRequest{
		Id: profileID,
	}))
	if err != nil {
		t.Fatalf("DeleteAuthProfile failed: %v", err)
	}

	// Verify all audit logs recorded in database
	logs, err := database.ListAuditLogs(ctx, db.ListAuditLogsParams{
		Limit:  100,
		Offset: 0,
	})
	if err != nil {
		t.Fatalf("ListAuditLogs failed: %v", err)
	}

	expectedActions := []string{
		"auth.setup_admin",
		"auth.login_failed",
		"auth.login",
		"auth_profile.create",
		"pool.create",
		"pool.update",
		"runner.terminate",
		"setting.update",
		"image.pull",
		"image.dismiss_update",
		"renovate.trigger",
		"pool.delete",
		"auth_profile.delete",
	}

	actionCounts := make(map[string]int)
	for _, l := range logs {
		actionCounts[l.Action]++

		// Verify action conforms to <resource>.<verb> dot-notation
		parts := strings.Split(l.Action, ".")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			t.Errorf("audit log action %q does not conform to <resource>.<verb> dot-notation", l.Action)
		}

		// Verify resource_type is set
		if !l.ResourceType.Valid || l.ResourceType.String == "" {
			t.Errorf("audit log for action %q missing resource_type", l.Action)
		}

		// Verify details is valid JSON
		if l.Details.Valid {
			var js map[string]any
			if err := json.Unmarshal([]byte(l.Details.String), &js); err != nil {
				t.Errorf("audit log for action %q has invalid JSON details: %v (raw: %s)", l.Action, err, l.Details.String)
			}
		} else {
			t.Errorf("audit log for action %q missing details JSON", l.Action)
		}
	}

	for _, expected := range expectedActions {
		if actionCounts[expected] == 0 {
			t.Errorf("expected audit log action %q was not recorded", expected)
		}
	}
}

func TestRecordAuditLogHelper(t *testing.T) {
	ctx := context.Background()
	database, _ := setupTestDB(t)

	// 1. Calling with nil database should not panic
	server.RecordAuditLog(ctx, nil, "test.action", "test_res", nil, nil)
	server.RecordAuditLogWithUser(ctx, nil, nil, "test.action", "test_res", nil, nil)

	// 2. Calling without user in context -> user_id is null
	resID := int64(42)
	server.RecordAuditLog(ctx, database, "pool.create", "runner_pool", &resID, map[string]any{"name": "test-pool"})

	logs, err := database.ListAuditLogs(ctx, db.ListAuditLogsParams{Limit: 1, Offset: 0})
	if err != nil || len(logs) != 1 {
		t.Fatalf("expected 1 audit log, got %+v (err: %v)", logs, err)
	}
	if logs[0].UserID.Valid {
		t.Errorf("expected null user_id without user context, got %d", logs[0].UserID.Int64)
	}
	if logs[0].Action != "pool.create" {
		t.Errorf("expected action pool.create, got %s", logs[0].Action)
	}
	if !logs[0].ResourceID.Valid || logs[0].ResourceID.Int64 != 42 {
		t.Errorf("expected resource_id 42, got %+v", logs[0].ResourceID)
	}

	// 3. Calling with user in context -> user_id is populated
	adminUser, err := database.CreateAdminUser(ctx, db.CreateAdminUserParams{
		Username:     "audit-admin",
		PasswordHash: "fakehash",
	})
	if err != nil {
		t.Fatalf("CreateAdminUser failed: %v", err)
	}

	userCtx := server.WithUserContext(ctx, &server.UserContext{
		UserID:   adminUser.ID,
		Username: adminUser.Username,
	})
	server.RecordAuditLog(userCtx, database, "pool.delete", "runner_pool", &resID, map[string]any{"deleted": true})

	logs, err = database.ListAuditLogs(ctx, db.ListAuditLogsParams{Limit: 1, Offset: 0})
	if err != nil || len(logs) != 1 {
		t.Fatalf("expected audit log, got %+v (err: %v)", logs, err)
	}
	if !logs[0].UserID.Valid || logs[0].UserID.Int64 != adminUser.ID {
		t.Errorf("expected user_id %d, got %+v", adminUser.ID, logs[0].UserID)
	}
	if logs[0].Action != "pool.delete" {
		t.Errorf("expected action pool.delete, got %s", logs[0].Action)
	}

	// 4. Calling RecordAuditLogWithUser with explicit user ID
	server.RecordAuditLogWithUser(ctx, database, &adminUser.ID, "auth.login", "admin_user", &adminUser.ID, map[string]any{"username": "explicit-user"})

	logs, err = database.ListAuditLogs(ctx, db.ListAuditLogsParams{Limit: 1, Offset: 0})
	if err != nil || len(logs) != 1 {
		t.Fatalf("expected audit log, got %+v (err: %v)", logs, err)
	}
	if !logs[0].UserID.Valid || logs[0].UserID.Int64 != adminUser.ID {
		t.Errorf("expected user_id %d, got %+v", adminUser.ID, logs[0].UserID)
	}
	if logs[0].Action != "auth.login" {
		t.Errorf("expected action auth.login, got %s", logs[0].Action)
	}
}

