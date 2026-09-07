package server_test

import (
	"context"
	"database/sql"
	"errors"
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

type mockRenovateExecutor struct {
	executeFn           func(ctx context.Context, poolID int64) (*db.RenovateRun, error)
	handleExitFn        func(ctx context.Context, containerID string, exitCode int, logPath string) (bool, error)
	lastExecutedPoolID  int64
}

func (m *mockRenovateExecutor) Execute(ctx context.Context, poolID int64) (*db.RenovateRun, error) {
	m.lastExecutedPoolID = poolID
	if m.executeFn != nil {
		return m.executeFn(ctx, poolID)
	}
	now := time.Now().UTC()
	return &db.RenovateRun{
		ID:        100,
		PoolID:    poolID,
		Status:    "running",
		StartedAt: now,
		Summary:   "Renovate task running",
	}, nil
}

func (m *mockRenovateExecutor) HandleContainerExit(ctx context.Context, containerID string, exitCode int, logPath string) (bool, error) {
	if m.handleExitFn != nil {
		return m.handleExitFn(ctx, containerID, exitCode, logPath)
	}
	return false, nil
}

type mockCronScheduler struct {
	nextRunFn func(poolID int64) (time.Time, error)
}

func (m *mockCronScheduler) NextRun(poolID int64) (time.Time, error) {
	if m.nextRunFn != nil {
		return m.nextRunFn(poolID)
	}
	return time.Time{}, errors.New("no next run")
}

func TestRenovateServiceLifecycle(t *testing.T) {
	ctx := context.Background()
	database, jwtSecret := setupTestDB(t)

	mockExec := &mockRenovateExecutor{}
	scheduledTime := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	mockCron := &mockCronScheduler{
		nextRunFn: func(poolID int64) (time.Time, error) {
			if poolID == 1 {
				return scheduledTime, nil
			}
			return time.Time{}, errors.New("job not found")
		},
	}

	srv := server.New(server.Options{
		Port:             8080,
		AuthDB:           database,
		PoolDB:           database,
		RenovateDB:       database,
		RenovateExecutor: mockExec,
		CronScheduler:    mockCron,
		JWTSigningSecret: jwtSecret,
	})

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Authenticate
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

	// Create auth profile and test pool
	authProfile, err := database.CreateAuthProfile(ctx, db.CreateAuthProfileParams{
		Name:           "test-auth-profile",
		AuthMethod:     "pat",
		TokenEncrypted: sql.NullString{String: "encrypted-token", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateAuthProfile failed: %v", err)
	}

	pool, err := database.CreateRunnerPool(ctx, db.CreateRunnerPoolParams{
		Name:                     "test-pool",
		Provider:                 "github",
		RepositoryUrl:            "https://github.com/org/repo",
		Scope:                    "repo",
		AuthProfileID:            authProfile.ID,
		MinIdleRunners:           2,
		MaxConcurrency:           5,
		Labels:                   "self-hosted",
		RunnerImage:              "ghcr.io/noosxe/runnero:latest",
		AllowDocker:              true,
		MaxRunnerLifetimeSeconds: 7200,
	})
	if err != nil {
		t.Fatalf("CreateRunnerPool failed: %v", err)
	}

	client := supervisorv1connect.NewRenovateServiceClient(ts.Client(), ts.URL)

	// 1. Validation: invalid pool_id
	req0 := connect.NewRequest(&supervisorv1.GetRenovateStatusRequest{PoolId: 0})
	req0.Header().Set("Cookie", "session_token="+rawCookie)
	_, err = client.GetRenovateStatus(ctx, req0)
	if err == nil || connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected CodeInvalidArgument for pool_id=0, got %v", err)
	}

	// 2. Non-existent pool
	req9999 := connect.NewRequest(&supervisorv1.GetRenovateStatusRequest{PoolId: 9999})
	req9999.Header().Set("Cookie", "session_token="+rawCookie)
	_, err = client.GetRenovateStatus(ctx, req9999)
	if err == nil || connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("expected CodeNotFound for pool_id=9999, got %v", err)
	}

	// 3. Initial status with no runs: LastRun should be nil, NextScheduledRun returned
	statusReq := connect.NewRequest(&supervisorv1.GetRenovateStatusRequest{PoolId: pool.ID})
	statusReq.Header().Set("Cookie", "session_token="+rawCookie)
	statusRes, err := client.GetRenovateStatus(ctx, statusReq)
	if err != nil {
		t.Fatalf("GetRenovateStatus failed: %v", err)
	}
	if statusRes.Msg.LastRun != nil {
		t.Fatalf("expected nil LastRun before any executions, got %+v", statusRes.Msg.LastRun)
	}
	if statusRes.Msg.NextScheduledRun != scheduledTime.Format(time.RFC3339) {
		t.Fatalf("next_scheduled_run = %q, want %q", statusRes.Msg.NextScheduledRun, scheduledTime.Format(time.RFC3339))
	}

	// 4. Trigger Renovate run without auth -> Unauthenticated
	unauthTrigger := connect.NewRequest(&supervisorv1.TriggerRenovateRunRequest{PoolId: pool.ID})
	_, err = client.TriggerRenovateRun(ctx, unauthTrigger)
	if err == nil || connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("expected CodeUnauthenticated without cookie, got %v", err)
	}

	// 5. Trigger Renovate run with auth -> Success
	triggerReq := connect.NewRequest(&supervisorv1.TriggerRenovateRunRequest{PoolId: pool.ID})
	triggerReq.Header().Set("Cookie", "session_token="+rawCookie)
	triggerRes, err := client.TriggerRenovateRun(ctx, triggerReq)
	if err != nil {
		t.Fatalf("TriggerRenovateRun failed: %v", err)
	}
	if !triggerRes.Msg.Success || triggerRes.Msg.RunId != 100 {
		t.Fatalf("unexpected TriggerRenovateRun response: %+v", triggerRes.Msg)
	}
	if mockExec.lastExecutedPoolID != pool.ID {
		t.Fatalf("executor executed pool %d, want %d", mockExec.lastExecutedPoolID, pool.ID)
	}

	// 6. Guard: triggering when run is already "running" -> FailedPrecondition
	// First record a "running" run in DB
	runningRun, err := database.CreateRenovateRun(ctx, db.CreateRenovateRunParams{
		PoolID:    pool.ID,
		Status:    "running",
		StartedAt: time.Now().UTC(),
		Summary:   "In progress",
	})
	if err != nil {
		t.Fatalf("CreateRenovateRun failed: %v", err)
	}

	dupTriggerReq := connect.NewRequest(&supervisorv1.TriggerRenovateRunRequest{PoolId: pool.ID})
	dupTriggerReq.Header().Set("Cookie", "session_token="+rawCookie)
	_, err = client.TriggerRenovateRun(ctx, dupTriggerReq)
	if err == nil || connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("expected CodeFailedPrecondition on concurrent run, got %v", err)
	}

	// Complete the running run
	completedTime := time.Now().UTC()
	_, err = database.CompleteRenovateRun(ctx, db.CompleteRenovateRunParams{
		Status:      "success",
		CompletedAt: sql.NullTime{Time: completedTime, Valid: true},
		Summary:     "2 pull requests created",
		ID:          runningRun.ID,
	})
	if err != nil {
		t.Fatalf("CompleteRenovateRun failed: %v", err)
	}

	// 7. Status now reflects the completed run
	statusRes, err = client.GetRenovateStatus(ctx, statusReq)
	if err != nil {
		t.Fatalf("GetRenovateStatus failed: %v", err)
	}
	if statusRes.Msg.LastRun == nil {
		t.Fatal("expected LastRun to be populated, got nil")
	}
	if statusRes.Msg.LastRun.Status != "success" || statusRes.Msg.LastRun.Summary != "2 pull requests created" {
		t.Fatalf("unexpected LastRun: %+v", statusRes.Msg.LastRun)
	}

	// 8. ListRenovateHistory
	histReq := connect.NewRequest(&supervisorv1.ListRenovateHistoryRequest{
		PoolId: pool.ID,
		Limit:  10,
		Offset: 0,
	})
	histReq.Header().Set("Cookie", "session_token="+rawCookie)
	histRes, err := client.ListRenovateHistory(ctx, histReq)
	if err != nil {
		t.Fatalf("ListRenovateHistory failed: %v", err)
	}
	if histRes.Msg.TotalCount != 1 || len(histRes.Msg.Runs) != 1 {
		t.Fatalf("expected 1 run in history, got %d (total %d)", len(histRes.Msg.Runs), histRes.Msg.TotalCount)
	}
	if histRes.Msg.Runs[0].Summary != "2 pull requests created" {
		t.Fatalf("unexpected summary in history: %s", histRes.Msg.Runs[0].Summary)
	}
}

func TestPoolServiceRenovateConfig(t *testing.T) {
	ctx := context.Background()
	database, jwtSecret := setupTestDB(t)

	srv := server.New(server.Options{
		Port:             8080,
		AuthDB:           database,
		PoolDB:           database,
		JWTSigningSecret: jwtSecret,
	})

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Authenticate
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
	if cookie == "" {
		t.Fatalf("Set-Cookie is empty on login: %+v", loginRes.Header())
	}
	rawCookie := strings.Split(strings.Split(cookie, ";")[0], "=")[1]

	authProfile, err := database.CreateAuthProfile(ctx, db.CreateAuthProfileParams{
		Name:           "test-auth",
		AuthMethod:     "pat",
		TokenEncrypted: sql.NullString{String: "enc", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateAuthProfile failed: %v", err)
	}

	poolClient := supervisorv1connect.NewPoolServiceClient(ts.Client(), ts.URL)

	// 1. Create pool with invalid cron schedule
	invalidReq := connect.NewRequest(&supervisorv1.CreatePoolRequest{
		Pool: &supervisorv1.Pool{
			Name:          "cron-invalid",
			Provider:      "github",
			RepositoryUrl: "https://github.com/org/repo",
			AuthProfileId: authProfile.ID,
			Renovate: &supervisorv1.RenovateConfig{
				Enabled:      true,
				CronSchedule: "not-a-cron-expr",
			},
		},
	})
	invalidReq.Header().Set("Cookie", "session_token="+rawCookie)
	_, err = poolClient.CreatePool(ctx, invalidReq)
	if err == nil || connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected CodeInvalidArgument for invalid cron schedule, got %v", err)
	}

	// 2. Create pool with valid Renovate config
	createReq := connect.NewRequest(&supervisorv1.CreatePoolRequest{
		Pool: &supervisorv1.Pool{
			Name:          "renovate-pool",
			Provider:      "github",
			RepositoryUrl: "https://github.com/org/repo",
			AuthProfileId: authProfile.ID,
			Renovate: &supervisorv1.RenovateConfig{
				Enabled:      true,
				CronSchedule: "0 4 * * *",
				Image:        "renovate/renovate:38",
			},
		},
	})
	createReq.Header().Set("Cookie", "session_token="+rawCookie)
	createRes, err := poolClient.CreatePool(ctx, createReq)
	if err != nil {
		t.Fatalf("CreatePool with Renovate failed: %v", err)
	}
	if createRes.Msg.Pool.Renovate == nil || !createRes.Msg.Pool.Renovate.Enabled || createRes.Msg.Pool.Renovate.CronSchedule != "0 4 * * *" {
		t.Fatalf("unexpected renovate config on created pool: %+v", createRes.Msg.Pool.Renovate)
	}

	// 3. ListPools returns the renovate config
	listReq := connect.NewRequest(&supervisorv1.ListPoolsRequest{})
	listReq.Header().Set("Cookie", "session_token="+rawCookie)
	listRes, err := poolClient.ListPools(ctx, listReq)
	if err != nil {
		t.Fatalf("ListPools failed: %v", err)
	}
	if len(listRes.Msg.Pools) != 1 || listRes.Msg.Pools[0].Renovate == nil || listRes.Msg.Pools[0].Renovate.CronSchedule != "0 4 * * *" {
		t.Fatalf("unexpected pool in ListPools: %+v", listRes.Msg.Pools)
	}

	// 4. UpdatePool updates Renovate config
	updateReq := connect.NewRequest(&supervisorv1.UpdatePoolRequest{
		Pool: &supervisorv1.Pool{
			Id:            createRes.Msg.Pool.Id,
			Name:          "renovate-pool",
			Provider:      "github",
			RepositoryUrl: "https://github.com/org/repo",
			AuthProfileId: authProfile.ID,
			Renovate: &supervisorv1.RenovateConfig{
				Enabled:      false,
				CronSchedule: "0 6 * * *",
				Image:        "renovate/renovate:latest",
			},
		},
	})
	updateReq.Header().Set("Cookie", "session_token="+rawCookie)
	updateRes, err := poolClient.UpdatePool(ctx, updateReq)
	if err != nil {
		t.Fatalf("UpdatePool with Renovate failed: %v", err)
	}
	if updateRes.Msg.Pool.Renovate == nil || updateRes.Msg.Pool.Renovate.Enabled || updateRes.Msg.Pool.Renovate.CronSchedule != "0 6 * * *" {
		t.Fatalf("unexpected updated renovate config: %+v", updateRes.Msg.Pool.Renovate)
	}
}
