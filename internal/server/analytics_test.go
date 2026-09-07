package server_test

import (
	"context"
	"database/sql"
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

type mockSystemStats struct {
	active int32
	idle   int32
}

func (m *mockSystemStats) SystemRunnerStats() (active int32, idle int32) {
	return m.active, m.idle
}

func TestAnalyticsJobHistoryAndStats(t *testing.T) {
	ctx := context.Background()
	database, jwtSecret := setupTestDB(t)

	stats := &mockSystemStats{
		active: 5,
		idle:   3,
	}

	srv := server.New(server.Options{
		Port:             8080,
		AuthDB:           database,
		AnalyticsDB:      database,
		SystemStats:      stats,
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

	// Seed auth profile and runner pools
	authProf, err := database.CreateAuthProfile(ctx, db.CreateAuthProfileParams{
		Name:           "analytics-auth",
		AuthMethod:     "pat",
		TokenEncrypted: sql.NullString{String: "enc", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateAuthProfile: %v", err)
	}

	p1, err := database.CreateRunnerPool(ctx, db.CreateRunnerPoolParams{
		Name:          "analytics-pool-1",
		Provider:      "github",
		RepositoryUrl: "https://github.com/org/repo1",
		Scope:         "repo",
		AuthProfileID: authProf.ID,
		Labels:        "linux",
		RunnerImage:   "img",
	})
	if err != nil {
		t.Fatalf("CreateRunnerPool 1: %v", err)
	}

	p2, err := database.CreateRunnerPool(ctx, db.CreateRunnerPoolParams{
		Name:          "analytics-pool-2",
		Provider:      "github",
		RepositoryUrl: "https://github.com/org/repo2",
		Scope:         "repo",
		AuthProfileID: authProf.ID,
		Labels:        "linux",
		RunnerImage:   "img",
	})
	if err != nil {
		t.Fatalf("CreateRunnerPool 2: %v", err)
	}

	// Seed job history in database
	now := time.Now().UTC()
	q1 := now.Add(-10 * time.Minute)
	s1 := now.Add(-9 * time.Minute)
	c1 := now.Add(-5 * time.Minute)

	// Pool 1: 2 successful jobs
	_, err = database.CreateJobHistory(ctx, db.CreateJobHistoryParams{
		PoolID:      p1.ID,
		RunnerName:  "runner-1",
		Status:      "success",
		QueuedAt:    sql.NullTime{Time: q1, Valid: true},
		StartedAt:   sql.NullTime{Time: s1, Valid: true},
		CompletedAt: sql.NullTime{Time: c1, Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateJobHistory: %v", err)
	}

	_, err = database.CreateJobHistory(ctx, db.CreateJobHistoryParams{
		PoolID:      p1.ID,
		RunnerName:  "runner-2",
		Status:      "success",
		QueuedAt:    sql.NullTime{Time: q1, Valid: true},
		StartedAt:   sql.NullTime{Time: s1, Valid: true},
		CompletedAt: sql.NullTime{Time: c1, Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateJobHistory: %v", err)
	}

	// Pool 2: 1 failed job
	_, err = database.CreateJobHistory(ctx, db.CreateJobHistoryParams{
		PoolID:      p2.ID,
		RunnerName:  "runner-3",
		Status:      "failure",
		QueuedAt:    sql.NullTime{Time: q1, Valid: true},
		StartedAt:   sql.NullTime{Time: s1, Valid: true},
		CompletedAt: sql.NullTime{Time: c1, Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateJobHistory: %v", err)
	}

	client := supervisorv1connect.NewAnalyticsServiceClient(ts.Client(), ts.URL)

	// 1. GetJobHistory for all pools (pool_id = 0)
	allReq := connect.NewRequest(&supervisorv1.GetJobHistoryRequest{
		PoolId: 0,
		Limit:  10,
		Offset: 0,
	})
	allReq.Header().Set("Cookie", "session_token="+rawCookie)
	allRes, err := client.GetJobHistory(ctx, allReq)
	if err != nil {
		t.Fatalf("GetJobHistory failed: %v", err)
	}
	if allRes.Msg.TotalCount != 3 || len(allRes.Msg.Jobs) != 3 {
		t.Fatalf("expected 3 jobs, got: total=%d, len=%d", allRes.Msg.TotalCount, len(allRes.Msg.Jobs))
	}

	// 2. GetJobHistory filtered by pool_id = p1.ID
	poolReq := connect.NewRequest(&supervisorv1.GetJobHistoryRequest{
		PoolId: p1.ID,
		Limit:  10,
	})
	poolReq.Header().Set("Cookie", "session_token="+rawCookie)
	poolRes, err := client.GetJobHistory(ctx, poolReq)
	if err != nil {
		t.Fatalf("GetJobHistory for pool 1 failed: %v", err)
	}
	if poolRes.Msg.TotalCount != 2 || len(poolRes.Msg.Jobs) != 2 {
		t.Fatalf("expected 2 jobs for pool 1, got total=%d", poolRes.Msg.TotalCount)
	}

	// 2a. Search by runner name
	searchReq := connect.NewRequest(&supervisorv1.GetJobHistoryRequest{
		Search: "runner-3",
	})
	searchReq.Header().Set("Cookie", "session_token="+rawCookie)
	searchRes, err := client.GetJobHistory(ctx, searchReq)
	if err != nil {
		t.Fatalf("search GetJobHistory failed: %v", err)
	}
	if searchRes.Msg.TotalCount != 1 || len(searchRes.Msg.Jobs) != 1 || searchRes.Msg.Jobs[0].RunnerName != "runner-3" {
		t.Fatalf("expected 1 runner-3 job, got: %+v", searchRes.Msg.Jobs)
	}
	if searchRes.Msg.Jobs[0].PoolName != "analytics-pool-2" {
		t.Errorf("expected pool name 'analytics-pool-2', got %s", searchRes.Msg.Jobs[0].PoolName)
	}
	if searchRes.Msg.Jobs[0].DurationSeconds <= 0 || searchRes.Msg.Jobs[0].QueueTimeSeconds <= 0 {
		t.Errorf("expected positive duration and queue time, got duration=%f, queue=%f", searchRes.Msg.Jobs[0].DurationSeconds, searchRes.Msg.Jobs[0].QueueTimeSeconds)
	}

	// 2b. Filter by status
	statusReq := connect.NewRequest(&supervisorv1.GetJobHistoryRequest{
		Status: "success",
	})
	statusReq.Header().Set("Cookie", "session_token="+rawCookie)
	statusRes, err := client.GetJobHistory(ctx, statusReq)
	if err != nil {
		t.Fatalf("status filter GetJobHistory failed: %v", err)
	}
	if statusRes.Msg.TotalCount != 2 || len(statusRes.Msg.Jobs) != 2 {
		t.Fatalf("expected 2 success jobs, got: %d", statusRes.Msg.TotalCount)
	}

	// 2c. Pagination: limit 1, offset 1
	pageReq := connect.NewRequest(&supervisorv1.GetJobHistoryRequest{
		Limit:  1,
		Offset: 1,
	})
	pageReq.Header().Set("Cookie", "session_token="+rawCookie)
	pageRes, err := client.GetJobHistory(ctx, pageReq)
	if err != nil {
		t.Fatalf("paginated GetJobHistory failed: %v", err)
	}
	if pageRes.Msg.TotalCount != 3 || len(pageRes.Msg.Jobs) != 1 {
		t.Fatalf("expected total 3, page length 1, got total=%d, len=%d", pageRes.Msg.TotalCount, len(pageRes.Msg.Jobs))
	}

	// 2d. GetJobRecord
	jobReq := connect.NewRequest(&supervisorv1.GetJobRecordRequest{
		JobId: searchRes.Msg.Jobs[0].Id,
	})
	jobReq.Header().Set("Cookie", "session_token="+rawCookie)
	jobRes, err := client.GetJobRecord(ctx, jobReq)
	if err != nil {
		t.Fatalf("GetJobRecord failed: %v", err)
	}
	if jobRes.Msg.Job.RunnerName != "runner-3" || jobRes.Msg.Job.PoolName != "analytics-pool-2" {
		t.Errorf("GetJobRecord mismatch: %+v", jobRes.Msg.Job)
	}

	// 3. GetSystemStats
	statsReq := connect.NewRequest(&supervisorv1.GetSystemStatsRequest{})
	statsReq.Header().Set("Cookie", "session_token="+rawCookie)
	statsRes, err := client.GetSystemStats(ctx, statsReq)
	if err != nil {
		t.Fatalf("GetSystemStats failed: %v", err)
	}

	resMsg := statsRes.Msg
	if resMsg.TotalActiveRunners != 5 || resMsg.TotalIdleRunners != 3 {
		t.Errorf("runner counts mismatch: active=%d, idle=%d", resMsg.TotalActiveRunners, resMsg.TotalIdleRunners)
	}
	if resMsg.TotalJobs_24H != 3 {
		t.Errorf("total_jobs_24h = %d, want 3", resMsg.TotalJobs_24H)
	}
	if resMsg.SuccessfulJobs_24H != 2 {
		t.Errorf("successful_jobs_24h = %d, want 2", resMsg.SuccessfulJobs_24H)
	}
	if resMsg.FailedJobs_24H != 1 {
		t.Errorf("failed_jobs_24h = %d, want 1", resMsg.FailedJobs_24H)
	}
	expectedRate := (2.0 / 3.0) * 100.0
	if resMsg.SuccessRatePercent < expectedRate-1.0 || resMsg.SuccessRatePercent > expectedRate+1.0 {
		t.Errorf("success_rate_percent = %f, want ~%f", resMsg.SuccessRatePercent, expectedRate)
	}
	if len(resMsg.QueueLatencyTrend) == 0 {
		t.Errorf("expected non-empty queue latency trend")
	}
	if resMsg.AverageQueueTimeSeconds <= 0 {
		t.Errorf("average_queue_time_seconds = %f, want > 0", resMsg.AverageQueueTimeSeconds)
	}
	if resMsg.AverageRuntimeSeconds <= 0 {
		t.Errorf("average_runtime_seconds = %f, want > 0", resMsg.AverageRuntimeSeconds)
	}
	if resMsg.HostArch != server.HostArch() || resMsg.HostOs != server.HostOS() {
		t.Errorf("expected host arch/os %s/%s, got %s/%s", server.HostArch(), server.HostOS(), resMsg.HostArch, resMsg.HostOs)
	}
}

func TestAnalyticsServiceWatchDashboard(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	database, jwtSecret := setupTestDB(t)
	stats := &mockSystemStats{
		active: 8,
		idle:   2,
	}

	srv := server.New(server.Options{
		Port:             8080,
		AuthDB:           database,
		PoolDB:           database,
		AnalyticsDB:      database,
		SystemStats:      stats,
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

	// Seed auth profile and runner pool
	authProf, err := database.CreateAuthProfile(ctx, db.CreateAuthProfileParams{
		Name:           "watch-dash-auth",
		AuthMethod:     "pat",
		TokenEncrypted: sql.NullString{String: "dummy-token", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateAuthProfile failed: %v", err)
	}

	pool, err := database.CreateRunnerPool(ctx, db.CreateRunnerPoolParams{
		Name:           "watch-dash-pool",
		Provider:       "github",
		RepositoryUrl:  "https://github.com/org/repo",
		AuthProfileID:  authProf.ID,
		Scope:          "repo",
		MinIdleRunners: 2,
		MaxConcurrency: 10,
	})
	if err != nil {
		t.Fatalf("CreateRunnerPool failed: %v", err)
	}

	// Seed Job History
	now := time.Now().UTC()
	_, err = database.CreateJobHistory(ctx, db.CreateJobHistoryParams{
		PoolID:     pool.ID,
		RunnerName: "runner-watch-1",
		Status:     "success",
		QueuedAt:   sql.NullTime{Time: now.Add(-10 * time.Minute), Valid: true},
		StartedAt:  sql.NullTime{Time: now.Add(-9 * time.Minute), Valid: true},
		CompletedAt: sql.NullTime{Time: now.Add(-5 * time.Minute), Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateJobHistory failed: %v", err)
	}

	client := supervisorv1connect.NewAnalyticsServiceClient(ts.Client(), ts.URL)
	watchReq := connect.NewRequest(&supervisorv1.WatchDashboardRequest{
		IntervalMs: 250,
	})
	watchReq.Header().Set("Cookie", "session_token="+rawCookie)

	stream, err := client.WatchDashboard(ctx, watchReq)
	if err != nil {
		t.Fatalf("WatchDashboard failed: %v", err)
	}
	defer func() { _ = stream.Close() }()

	// First message: immediate snapshot
	if !stream.Receive() {
		t.Fatalf("expected initial message from WatchDashboard, got none (err: %v)", stream.Err())
	}

	msg := stream.Msg()
	if msg.Stats == nil {
		t.Fatalf("expected non-nil stats in WatchDashboard")
	}
	if msg.Stats.TotalActiveRunners != 8 || msg.Stats.TotalIdleRunners != 2 {
		t.Errorf("stats mismatch: active=%d, idle=%d", msg.Stats.TotalActiveRunners, msg.Stats.TotalIdleRunners)
	}
	if len(msg.Pools) != 1 || msg.Pools[0].Name != "watch-dash-pool" {
		t.Errorf("expected 1 pool named 'watch-dash-pool', got: %+v", msg.Pools)
	}
	if len(msg.RecentJobs) != 1 || msg.RecentJobs[0].RunnerName != "runner-watch-1" {
		t.Errorf("expected 1 recent job with runner 'runner-watch-1', got: %+v", msg.RecentJobs)
	}

	// Cancel context to ensure clean shutdown
	cancel()
	for stream.Receive() {
		// drain remaining
	}
	if err := stream.Err(); err != nil && !strings.Contains(err.Error(), "canceled") {
		t.Errorf("unexpected error on stream cancel: %v", err)
	}
}
