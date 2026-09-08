package server_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/noosxe/runnero/internal/db"
	supervisorv1 "github.com/noosxe/runnero/internal/pb/supervisor/v1"
	"github.com/noosxe/runnero/internal/pb/supervisor/v1/supervisorv1connect"
	"github.com/noosxe/runnero/internal/provider"
	"github.com/noosxe/runnero/internal/server"
)

type mockStatsProvider struct {
	mu           sync.Mutex
	reloadsCount int
	activeCounts map[int64]int32
	idleCounts   map[int64]int32
	diagnostics  map[int64]server.PoolDiagnostics
	recycled     []int64
}

func newMockStatsProvider() *mockStatsProvider {
	return &mockStatsProvider{
		activeCounts: make(map[int64]int32),
		idleCounts:   make(map[int64]int32),
		diagnostics:  make(map[int64]server.PoolDiagnostics),
	}
}

func (m *mockStatsProvider) PoolStats(poolID int64) (active int32, idle int32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.activeCounts[poolID], m.idleCounts[poolID]
}

func (m *mockStatsProvider) PoolDiagnostics(poolID int64) server.PoolDiagnostics {
	m.mu.Lock()
	defer m.mu.Unlock()
	if diag, ok := m.diagnostics[poolID]; ok {
		return diag
	}
	return server.PoolDiagnostics{
		HealthStatus:  "healthy",
		CurrentIntent: "Mock healthy",
	}
}

func (m *mockStatsProvider) Reload(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reloadsCount++
	return nil
}

// RecycleIdleRunners records recycle requests so tests can assert the
// spawn-identity/rename recycling behavior of UpdatePool (docs/22 §5.2).
func (m *mockStatsProvider) RecycleIdleRunners(ctx context.Context, poolID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recycled = append(m.recycled, poolID)
	return nil
}

// recycledCount reports how often RecycleIdleRunners was called for poolName.
func (m *mockStatsProvider) recycledCount(poolID int64) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, p := range m.recycled {
		if p == poolID {
			n++
		}
	}
	return n
}

func TestPoolServiceCRUDAndValidation(t *testing.T) {
	ctx := context.Background()
	database, jwtSecret := setupTestDB(t)
	stats := newMockStatsProvider()

	srv := server.New(server.Options{
		Port:             8080,
		AuthDB:           database,
		PoolDB:           database,
		PoolStats:        stats,
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

	// Create test auth profile for foreign key requirement
	authProfile, err := database.CreateAuthProfile(ctx, db.CreateAuthProfileParams{
		Name:           "test-auth-profile",
		AuthMethod:     "pat",
		TokenEncrypted: sql.NullString{String: "encrypted-token", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateAuthProfile failed: %v", err)
	}

	client := supervisorv1connect.NewPoolServiceClient(ts.Client(), ts.URL)

	// 1. Validation: Gitea / Forgejo require allow_docker=true
	giteaReq := connect.NewRequest(&supervisorv1.CreatePoolRequest{
		Pool: &supervisorv1.Pool{
			Name:          "gitea-pool",
			Provider:      "gitea",
			RepositoryUrl: "https://gitea.local/owner/repo",
			AuthProfileId: authProfile.ID,
			AllowDocker:   false, // Invalid!
		},
	})
	giteaReq.Header().Set("Cookie", "session_token="+rawCookie)
	_, err = client.CreatePool(ctx, giteaReq)
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("Gitea allow_docker=false want CodeInvalidArgument, got: %v", err)
	}

	// 2. Validation: invalid provider
	badProviderReq := connect.NewRequest(&supervisorv1.CreatePoolRequest{
		Pool: &supervisorv1.Pool{
			Name:          "bad-pool",
			Provider:      "bitbucket",
			RepositoryUrl: "https://bitbucket.org/owner/repo",
			AuthProfileId: authProfile.ID,
		},
	})
	badProviderReq.Header().Set("Cookie", "session_token="+rawCookie)
	_, err = client.CreatePool(ctx, badProviderReq)
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("Unsupported provider want CodeInvalidArgument, got: %v", err)
	}

	// 2b. Validation: non-existent auth_profile_id
	badAuthReq := connect.NewRequest(&supervisorv1.CreatePoolRequest{
		Pool: &supervisorv1.Pool{
			Name:          "bad-auth-pool",
			Provider:      "github",
			RepositoryUrl: "https://github.com/org/repo",
			AuthProfileId: 99999,
		},
	})
	badAuthReq.Header().Set("Cookie", "session_token="+rawCookie)
	_, err = client.CreatePool(ctx, badAuthReq)
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("Non-existent auth_profile_id want CodeInvalidArgument, got: %v", err)
	}

	// 2c. Validation: reject mixing repos and orgs
	mixedScopeReq := connect.NewRequest(&supervisorv1.CreatePoolRequest{
		Pool: &supervisorv1.Pool{
			Name:          "mixed-scope-pool",
			Provider:      "github",
			Scope:         "repo",
			AuthProfileId: authProfile.ID,
			TargetUrls: []string{
				"https://github.com/acme/repo-one",
				"https://github.com/acme", // Org URL in a repo pool
			},
		},
	})
	mixedScopeReq.Header().Set("Cookie", "session_token="+rawCookie)
	_, err = client.CreatePool(ctx, mixedScopeReq)
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("Mixed scope target want CodeInvalidArgument, got: %v", err)
	}

	// 3. CreatePool valid GitHub pool
	createReq := connect.NewRequest(&supervisorv1.CreatePoolRequest{
		Pool: &supervisorv1.Pool{
			Name:                     "github-arm64",
			Provider:                 "github",
			RepositoryUrl:            "https://github.com/org/repo",
			Scope:                    "repo",
			AuthProfileId:            authProfile.ID,
			MinIdleRunners:           2,
			MaxConcurrency:           10,
			Labels:                   []string{"self-hosted", "linux", "arm64"},
			RunnerImage:              "ghcr.io/noosxe/runnero:latest",
			AllowDocker:              true,
			CpuLimit:                 "2.0",
			MemoryLimit:              "4Gi",
			MaxRunnerLifetimeSeconds: 3600,
		},
	})
	createReq.Header().Set("Cookie", "session_token="+rawCookie)
	createRes, err := client.CreatePool(ctx, createReq)
	if err != nil {
		t.Fatalf("CreatePool failed: %v", err)
	}

	createdPool := createRes.Msg.Pool
	if createdPool.Id <= 0 || createdPool.Name != "github-arm64" {
		t.Fatalf("unexpected created pool: %+v", createdPool)
	}
	stats.activeCounts[createdPool.Id] = 3
	stats.idleCounts[createdPool.Id] = 2
	if len(createdPool.Labels) != 3 || createdPool.Labels[2] != "arm64" {
		t.Errorf("expected 3 labels, got: %+v", createdPool.Labels)
	}
	listReq0 := connect.NewRequest(&supervisorv1.ListPoolsRequest{})
	listReq0.Header().Set("Cookie", "session_token="+rawCookie)
	listRes0, err := client.ListPools(ctx, listReq0)
	if err != nil {
		t.Fatalf("ListPools failed: %v", err)
	}
	if len(listRes0.Msg.Pools) != 1 {
		t.Fatalf("expected 1 pool in list, got %d", len(listRes0.Msg.Pools))
	}
	if p0 := listRes0.Msg.Pools[0]; p0.ActiveRunners != 3 || p0.IdleRunners != 2 {
		t.Errorf("stats not populated properly: active=%d, idle=%d", p0.ActiveRunners, p0.IdleRunners)
	}

	// Verify reload was triggered and audit log was recorded
	stats.mu.Lock()
	if stats.reloadsCount != 1 {
		t.Errorf("expected 1 reload, got: %d", stats.reloadsCount)
	}
	stats.mu.Unlock()

	auditLogs, err := database.ListAuditLogs(ctx, db.ListAuditLogsParams{Limit: 10, Offset: 0})
	if err != nil || len(auditLogs) == 0 || auditLogs[0].Action != "pool.create" {
		t.Fatalf("expected pool.create audit log, got: %+v", auditLogs)
	}

	// 4. ListPools returns the created pool with stats
	listReq := connect.NewRequest(&supervisorv1.ListPoolsRequest{})
	listReq.Header().Set("Cookie", "session_token="+rawCookie)
	listRes, err := client.ListPools(ctx, listReq)
	if err != nil {
		t.Fatalf("ListPools failed: %v", err)
	}
	if len(listRes.Msg.Pools) != 1 || listRes.Msg.Pools[0].Name != "github-arm64" {
		t.Fatalf("expected 1 pool, got: %+v", listRes.Msg.Pools)
	}
	if listRes.Msg.Pools[0].ActiveRunners != 3 || listRes.Msg.Pools[0].IdleRunners != 2 {
		t.Errorf("ListPools stats mismatch: %+v", listRes.Msg.Pools[0])
	}

	// 5. UpdatePool updates properties and triggers reload
	updateReq := connect.NewRequest(&supervisorv1.UpdatePoolRequest{
		Pool: &supervisorv1.Pool{
			Id:                       createdPool.Id,
			Name:                     "github-arm64",
			Provider:                 "github",
			RepositoryUrl:            "https://github.com/org",
			Scope:                    "org",
			AuthProfileId:            authProfile.ID,
			MinIdleRunners:           4,
			MaxConcurrency:           15,
			Labels:                   []string{"self-hosted", "linux", "arm64", "gpu"},
			RunnerImage:              "ghcr.io/noosxe/runnero:v2",
			AllowDocker:              true,
			CpuLimit:                 "4.0",
			MemoryLimit:              "8Gi",
			MaxRunnerLifetimeSeconds: 7200,
		},
	})
	updateReq.Header().Set("Cookie", "session_token="+rawCookie)
	updateRes, err := client.UpdatePool(ctx, updateReq)
	if err != nil {
		t.Fatalf("UpdatePool failed: %v", err)
	}
	if updateRes.Msg.Pool.Scope != "org" || updateRes.Msg.Pool.MinIdleRunners != 4 {
		t.Errorf("UpdatePool mismatch: %+v", updateRes.Msg.Pool)
	}

	stats.mu.Lock()
	if stats.reloadsCount != 2 {
		t.Errorf("expected 2 reloads, got: %d", stats.reloadsCount)
	}
	stats.mu.Unlock()

	// 6. DeletePool removes the pool and triggers reload
	deleteReq := connect.NewRequest(&supervisorv1.DeletePoolRequest{
		Id: createdPool.Id,
	})
	deleteReq.Header().Set("Cookie", "session_token="+rawCookie)
	delRes, err := client.DeletePool(ctx, deleteReq)
	if err != nil {
		t.Fatalf("DeletePool failed: %v", err)
	}
	if !delRes.Msg.Success {
		t.Errorf("DeletePool want success=true")
	}

	stats.mu.Lock()
	if stats.reloadsCount != 3 {
		t.Errorf("expected 3 reloads, got: %d", stats.reloadsCount)
	}
	stats.mu.Unlock()

	// Verify pool list is empty now
	listRes2, err := client.ListPools(ctx, listReq)
	if err != nil || len(listRes2.Msg.Pools) != 0 {
		t.Fatalf("expected empty pool list after delete, got: %+v (err %v)", listRes2.Msg.Pools, err)
	}

	// Deleting again returns NotFound
	_, err = client.DeletePool(ctx, deleteReq)
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("second DeletePool want CodeNotFound, got: %v", err)
	}
}

func TestPoolServiceWatchPools(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	database, jwtSecret := setupTestDB(t)
	stats := newMockStatsProvider()

	srv := server.New(server.Options{
		Port:             8080,
		AuthDB:           database,
		PoolDB:           database,
		PoolStats:        stats,
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
	rawCookie := strings.Split(strings.Split(loginRes.Header().Get("Set-Cookie"), ";")[0], "=")[1]

	// Create Auth Profile
	authProf, err := database.CreateAuthProfile(ctx, db.CreateAuthProfileParams{
		Name:           "watch-auth",
		AuthMethod:     "pat",
		TokenEncrypted: sql.NullString{String: "dummy-token", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateAuthProfile failed: %v", err)
	}

	// Create Pool
	client := supervisorv1connect.NewPoolServiceClient(ts.Client(), ts.URL)
	createReq := connect.NewRequest(&supervisorv1.CreatePoolRequest{
		Pool: &supervisorv1.Pool{
			Name:           "watch-pool",
			Provider:       "github",
			RepositoryUrl:  "https://github.com/org/repo",
			AuthProfileId:  authProf.ID,
			MinIdleRunners: 1,
			MaxConcurrency: 5,
		},
	})
	createReq.Header().Set("Cookie", "session_token="+rawCookie)
	createRes, err := client.CreatePool(ctx, createReq)
	if err != nil {
		t.Fatalf("CreatePool failed: %v", err)
	}
	stats.activeCounts[createRes.Msg.Pool.Id] = 4
	stats.idleCounts[createRes.Msg.Pool.Id] = 1

	// Watch Pools stream
	watchReq := connect.NewRequest(&supervisorv1.WatchPoolsRequest{
		IntervalMs: 250,
	})
	watchReq.Header().Set("Cookie", "session_token="+rawCookie)

	stream, err := client.WatchPools(ctx, watchReq)
	if err != nil {
		t.Fatalf("WatchPools failed: %v", err)
	}
	defer func() { _ = stream.Close() }()

	// First message: immediate snapshot
	if !stream.Receive() {
		t.Fatalf("expected initial message from WatchPools, got none (err: %v)", stream.Err())
	}

	msg := stream.Msg()
	if len(msg.Pools) != 1 {
		t.Fatalf("expected 1 pool, got %d", len(msg.Pools))
	}
	if msg.Pools[0].Name != "watch-pool" {
		t.Errorf("expected pool name 'watch-pool', got %s", msg.Pools[0].Name)
	}
	if msg.Pools[0].ActiveRunners != 4 || msg.Pools[0].IdleRunners != 1 {
		t.Errorf("expected 4 active, 1 idle, got active=%d, idle=%d", msg.Pools[0].ActiveRunners, msg.Pools[0].IdleRunners)
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

type mockRunnerManager struct {
	mu         sync.Mutex
	runners    map[int64][]server.RunnerInstanceInfo
	terminated []string
}

func newMockRunnerManager() *mockRunnerManager {
	return &mockRunnerManager{
		runners: make(map[int64][]server.RunnerInstanceInfo),
	}
}

func (m *mockRunnerManager) PoolRunners(poolID int64) []server.RunnerInstanceInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.runners[poolID]
}

func (m *mockRunnerManager) TerminateRunner(ctx context.Context, poolID int64, containerID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.terminated = append(m.terminated, containerID)
	list := m.runners[poolID]
	filtered := make([]server.RunnerInstanceInfo, 0, len(list))
	for _, r := range list {
		if r.ID != containerID {
			filtered = append(filtered, r)
		}
	}
	m.runners[poolID] = filtered
	return nil
}

func TestPoolServiceListRunnersAndTerminate(t *testing.T) {
	ctx := context.Background()
	database, jwtSecret := setupTestDB(t)
	runnerMgr := newMockRunnerManager()

	runnerMgr.runners[1] = []server.RunnerInstanceInfo{
		{
			ID:        "cnt-alpha",
			Name:      "runnero-runner-alpha",
			PoolName:  "runner-mgmt-pool",
			State:     "running",
			IPAddress: "172.18.0.2",
			IsBusy:    true,
		},
		{
			ID:        "cnt-beta",
			Name:      "runnero-runner-beta",
			PoolName:  "runner-mgmt-pool",
			State:     "running",
			IPAddress: "172.18.0.3",
			IsBusy:    false,
		},
	}

	srv := server.New(server.Options{
		Port:             8080,
		AuthDB:           database,
		PoolDB:           database,
		RunnerMgr:        runnerMgr,
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
	rawCookie := strings.Split(strings.Split(loginRes.Header().Get("Set-Cookie"), ";")[0], "=")[1]

	// Create Auth Profile and Pool
	authProf, err := database.CreateAuthProfile(ctx, db.CreateAuthProfileParams{
		Name:           "mgmt-auth",
		AuthMethod:     "pat",
		TokenEncrypted: sql.NullString{String: "token", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateAuthProfile failed: %v", err)
	}

	pool, err := database.CreateRunnerPool(ctx, db.CreateRunnerPoolParams{
		Name:           "runner-mgmt-pool",
		Provider:       "github",
		RepositoryUrl:  "https://github.com/org/repo",
		AuthProfileID:  authProf.ID,
		Scope:          "repo",
		MinIdleRunners: 1,
		MaxConcurrency: 5,
	})
	if err != nil {
		t.Fatalf("CreateRunnerPool failed: %v", err)
	}

	client := supervisorv1connect.NewPoolServiceClient(ts.Client(), ts.URL)

	// List runners
	listReq := connect.NewRequest(&supervisorv1.ListRunnersRequest{
		PoolId: pool.ID,
	})
	listReq.Header().Set("Cookie", "session_token="+rawCookie)

	listRes, err := client.ListRunners(ctx, listReq)
	if err != nil {
		t.Fatalf("ListRunners failed: %v", err)
	}
	if len(listRes.Msg.Runners) != 2 {
		t.Fatalf("expected 2 runners, got %d", len(listRes.Msg.Runners))
	}
	if listRes.Msg.Runners[0].Status != "busy" || listRes.Msg.Runners[1].Status != "idle" {
		t.Errorf("runner statuses mismatch: %+v", listRes.Msg.Runners)
	}

	// Terminate cnt-alpha
	termReq := connect.NewRequest(&supervisorv1.TerminateRunnerRequest{
		PoolId:      pool.ID,
		ContainerId: "cnt-alpha",
	})
	termReq.Header().Set("Cookie", "session_token="+rawCookie)

	termRes, err := client.TerminateRunner(ctx, termReq)
	if err != nil {
		t.Fatalf("TerminateRunner failed: %v", err)
	}
	if !termRes.Msg.Success {
		t.Errorf("expected success=true")
	}

	// Verify only cnt-beta remains
	listRes2, err := client.ListRunners(ctx, listReq)
	if err != nil {
		t.Fatalf("second ListRunners failed: %v", err)
	}
	if len(listRes2.Msg.Runners) != 1 || listRes2.Msg.Runners[0].ContainerId != "cnt-beta" {
		t.Fatalf("expected only cnt-beta to remain, got: %+v", listRes2.Msg.Runners)
	}
}

func TestPoolServiceWatchRunners(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	database, jwtSecret := setupTestDB(t)
	runnerMgr := newMockRunnerManager()
	streamRunners := []server.RunnerInstanceInfo{
		{
			ID:        "stream-cnt-1",
			Name:      "runnero-stream-1",
			PoolName:  "stream-pool",
			State:     "running",
			IPAddress: "172.18.0.9",
			IsBusy:    true,
		},
	}

	srv := server.New(server.Options{
		Port:             8080,
		AuthDB:           database,
		PoolDB:           database,
		RunnerMgr:        runnerMgr,
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
	rawCookie := strings.Split(strings.Split(loginRes.Header().Get("Set-Cookie"), ";")[0], "=")[1]

	authProf, err := database.CreateAuthProfile(ctx, db.CreateAuthProfileParams{
		Name:           "stream-auth",
		AuthMethod:     "pat",
		TokenEncrypted: sql.NullString{String: "token", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateAuthProfile failed: %v", err)
	}

	pool, err := database.CreateRunnerPool(ctx, db.CreateRunnerPoolParams{
		Name:           "stream-pool",
		Provider:       "github",
		RepositoryUrl:  "https://github.com/org/repo",
		AuthProfileID:  authProf.ID,
		Scope:          "repo",
		MinIdleRunners: 1,
		MaxConcurrency: 5,
	})
	if err != nil {
		t.Fatalf("CreateRunnerPool failed: %v", err)
	}
	runnerMgr.runners[pool.ID] = streamRunners

	client := supervisorv1connect.NewPoolServiceClient(ts.Client(), ts.URL)
	watchReq := connect.NewRequest(&supervisorv1.WatchRunnersRequest{
		PoolId:     pool.ID,
		IntervalMs: 250,
	})
	watchReq.Header().Set("Cookie", "session_token="+rawCookie)

	stream, err := client.WatchRunners(ctx, watchReq)
	if err != nil {
		t.Fatalf("WatchRunners failed: %v", err)
	}
	defer func() { _ = stream.Close() }()

	if !stream.Receive() {
		t.Fatalf("expected initial message from WatchRunners, got none (err: %v)", stream.Err())
	}

	msg := stream.Msg()
	if len(msg.Runners) != 1 || msg.Runners[0].ContainerId != "stream-cnt-1" {
		t.Fatalf("expected stream-cnt-1, got: %+v", msg.Runners)
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

func TestPoolServiceDiscoverTargets(t *testing.T) {
	ctx := context.Background()
	database, jwtSecret := setupTestDB(t)

	// Create auth profile
	prof, err := database.CreateEncryptedAuthProfile(ctx, "test-gh-profile", "pat", sql.NullInt64{}, "", "secret-token")
	if err != nil {
		t.Fatalf("CreateEncryptedAuthProfile failed: %v", err)
	}

	poolSvc := server.NewPoolService(database, nil, nil, server.WithDiscoverer(func(ctx context.Context, p db.DecryptedAuthProfile, scope string) (*server.DiscoveryResult, error) {
		if scope == "org" {
			return &server.DiscoveryResult{
				Targets: []provider.DiscoveredTarget{
					{
						Name:        "acme-org",
						FullName:    "acme-org",
						HTMLURL:     "https://github.com/acme-org",
						Description: "Acme Corp Org",
						AvatarURL:   "https://avatars.example.com/acme",
					},
				},
			}, nil
		}
		return &server.DiscoveryResult{
			InstallURL: "https://github.com/apps/test-app/installations/new",
			Installations: []provider.AppInstallation{
				{
					ID:                  101,
					AccountLogin:        "acme-org",
					AccountType:         "Organization",
					HTMLURL:             "https://github.com/organizations/acme-org/settings/installations/101",
					RepositorySelection: "selected",
				},
			},
			Targets: []provider.DiscoveredTarget{
				{
					Name:        "repo-alpha",
					FullName:    "acme-org/repo-alpha",
					HTMLURL:     "https://github.com/acme-org/repo-alpha",
					Description: "First repo",
					IsPrivate:   true,
				},
				{
					Name:        "repo-beta",
					FullName:    "acme-org/repo-beta",
					HTMLURL:     "https://github.com/acme-org/repo-beta",
					Description: "Second repo",
					IsPrivate:   false,
				},
			},
		}, nil
	}))

	srv := server.New(server.Options{
		Port:             8080,
		AuthDB:           database,
		PoolDB:           database,
		JWTSigningSecret: jwtSecret,
	})
	path, handler := supervisorv1connect.NewPoolServiceHandler(poolSvc, srv.ConnectHandlerOptions()...)
	srv.MountConnectHandler(path, handler)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	authClient := supervisorv1connect.NewAuthServiceClient(ts.Client(), ts.URL)
	_, _ = authClient.SetupAdmin(ctx, connect.NewRequest(&supervisorv1.SetupAdminRequest{
		Username: "admin",
		Password: "password123",
	}))
	loginRes, err := authClient.Login(ctx, connect.NewRequest(&supervisorv1.LoginRequest{
		Username: "admin",
		Password: "password123",
	}))
	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}
	cookie := loginRes.Header().Get("Set-Cookie")
	rawCookie := strings.Split(strings.Split(cookie, ";")[0], "=")[1]

	client := supervisorv1connect.NewPoolServiceClient(ts.Client(), ts.URL)

	// 1. Missing AuthProfileId
	badReq := connect.NewRequest(&supervisorv1.DiscoverTargetsRequest{
		AuthProfileId: 0,
		Scope:         "repo",
	})
	badReq.Header().Set("Cookie", "session_token="+rawCookie)
	_, err = client.DiscoverTargets(ctx, badReq)
	if err == nil {
		t.Fatal("expected error with auth_profile_id = 0, got nil")
	}

	// 2. Invalid Scope
	invalidScopeReq := connect.NewRequest(&supervisorv1.DiscoverTargetsRequest{
		AuthProfileId: prof.ID,
		Scope:         "invalid_scope",
	})
	invalidScopeReq.Header().Set("Cookie", "session_token="+rawCookie)
	_, err = client.DiscoverTargets(ctx, invalidScopeReq)
	if err == nil {
		t.Fatal("expected error with invalid scope, got nil")
	}

	// 3. Discover Repositories
	repoReq := connect.NewRequest(&supervisorv1.DiscoverTargetsRequest{
		AuthProfileId: prof.ID,
		Scope:         "repo",
	})
	repoReq.Header().Set("Cookie", "session_token="+rawCookie)
	repoRes, err := client.DiscoverTargets(ctx, repoReq)
	if err != nil {
		t.Fatalf("DiscoverTargets repos failed: %v", err)
	}
	if len(repoRes.Msg.Targets) != 2 {
		t.Fatalf("expected 2 discovered repos, got %d", len(repoRes.Msg.Targets))
	}
	if repoRes.Msg.Targets[0].Name != "repo-alpha" || !repoRes.Msg.Targets[0].IsPrivate {
		t.Errorf("unexpected repo target 0: %+v", repoRes.Msg.Targets[0])
	}
	if repoRes.Msg.Targets[1].Name != "repo-beta" || repoRes.Msg.Targets[1].IsPrivate {
		t.Errorf("unexpected repo target 1: %+v", repoRes.Msg.Targets[1])
	}
	if repoRes.Msg.InstallUrl != "https://github.com/apps/test-app/installations/new" {
		t.Errorf("unexpected install_url: %s", repoRes.Msg.InstallUrl)
	}
	if len(repoRes.Msg.Installations) != 1 || repoRes.Msg.Installations[0].AccountLogin != "acme-org" {
		t.Errorf("unexpected installations: %+v", repoRes.Msg.Installations)
	}

	// 4. Discover Organizations
	orgReq := connect.NewRequest(&supervisorv1.DiscoverTargetsRequest{
		AuthProfileId: prof.ID,
		Scope:         "org",
	})
	orgReq.Header().Set("Cookie", "session_token="+rawCookie)
	orgRes, err := client.DiscoverTargets(ctx, orgReq)
	if err != nil {
		t.Fatalf("DiscoverTargets orgs failed: %v", err)
	}
	if len(orgRes.Msg.Targets) != 1 || orgRes.Msg.Targets[0].Name != "acme-org" {
		t.Fatalf("expected 1 discovered org (acme-org), got %+v", orgRes.Msg.Targets)
	}
}

func TestPoolServiceOperationalDiagnostics(t *testing.T) {
	ctx := context.Background()
	database, jwtSecret := setupTestDB(t)

	authProf, err := database.CreateAuthProfile(ctx, db.CreateAuthProfileParams{
		Name:           "diag-auth",
		AuthMethod:     "pat",
		TokenEncrypted: sql.NullString{String: "token", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateAuthProfile failed: %v", err)
	}

	// Create test pool
	p, err := database.CreateRunnerPool(ctx, db.CreateRunnerPoolParams{
		Name:           "diag-pool",
		Provider:       "github",
		RepositoryUrl:  "https://github.com/org/diag",
		Scope:          "repo",
		MinIdleRunners: 2,
		MaxConcurrency: 5,
		RunnerImage:    "ghcr.io/noosxe/runnero:latest",
		AuthProfileID:  authProf.ID,
	})
	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}

	stats := newMockStatsProvider()
	now := time.Now().UTC().Truncate(time.Second)
	stats.diagnostics[p.ID] = server.PoolDiagnostics{
		HealthStatus:       "degraded",
		CurrentIntent:      "Reconciling warm pool",
		LastError:          "Decryption failure on master key",
		LastErrorCode:      "AUTH_DECRYPTION_FAILED",
		LastErrorTimestamp: now,
		LastReconciledAt:   now,
	}

	srv := server.New(server.Options{
		Port:             8080,
		AuthDB:           database,
		PoolDB:           database,
		PoolStats:        stats,
		JWTSigningSecret: jwtSecret,
	})

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

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
	rawCookie := strings.Split(strings.Split(loginRes.Header().Get("Set-Cookie"), ";")[0], "=")[1]

	client := supervisorv1connect.NewPoolServiceClient(ts.Client(), ts.URL)

	// 1. ListPools returns diagnostics
	listReq := connect.NewRequest(&supervisorv1.ListPoolsRequest{})
	listReq.Header().Set("Cookie", "session_token="+rawCookie)
	listRes, err := client.ListPools(ctx, listReq)
	if err != nil {
		t.Fatalf("ListPools failed: %v", err)
	}
	if len(listRes.Msg.Pools) != 1 {
		t.Fatalf("expected 1 pool, got %d", len(listRes.Msg.Pools))
	}
	poolProto := listRes.Msg.Pools[0]
	if poolProto.HealthStatus != supervisorv1.PoolHealthStatus_POOL_HEALTH_STATUS_DEGRADED {
		t.Errorf("expected health status DEGRADED, got %v", poolProto.HealthStatus)
	}
	if poolProto.CurrentIntent != "Reconciling warm pool" {
		t.Errorf("expected current_intent 'Reconciling warm pool', got %q", poolProto.CurrentIntent)
	}
	if poolProto.LastErrorCode != "AUTH_DECRYPTION_FAILED" {
		t.Errorf("expected last_error_code 'AUTH_DECRYPTION_FAILED', got %q", poolProto.LastErrorCode)
	}
	if poolProto.LastError != "Decryption failure on master key" {
		t.Errorf("expected last_error 'Decryption failure on master key', got %q", poolProto.LastError)
	}
	if poolProto.LastErrorTimestamp == "" || poolProto.LastReconciledAt == "" {
		t.Errorf("expected timestamps to be populated, got error_time=%q, recon_time=%q", poolProto.LastErrorTimestamp, poolProto.LastReconciledAt)
	}

	// 2. WatchRunners streams diagnostics
	watchCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	watchReq := connect.NewRequest(&supervisorv1.WatchRunnersRequest{
		PoolId:     p.ID,
		IntervalMs: 500,
	})
	watchReq.Header().Set("Cookie", "session_token="+rawCookie)

	stream, err := client.WatchRunners(watchCtx, watchReq)
	if err != nil {
		t.Fatalf("WatchRunners failed: %v", err)
	}
	if stream.Receive() {
		msg := stream.Msg()
		if msg.HealthStatus != supervisorv1.PoolHealthStatus_POOL_HEALTH_STATUS_DEGRADED {
			t.Errorf("expected stream health status DEGRADED, got %v", msg.HealthStatus)
		}
		if msg.LastErrorCode != "AUTH_DECRYPTION_FAILED" {
			t.Errorf("expected stream last_error_code 'AUTH_DECRYPTION_FAILED', got %q", msg.LastErrorCode)
		}
		if msg.CurrentIntent != "Reconciling warm pool" {
			t.Errorf("expected stream current_intent 'Reconciling warm pool', got %q", msg.CurrentIntent)
		}
	} else {
		t.Fatalf("expected stream message, got none (err: %v)", stream.Err())
	}
}

// startPoolEditTestServer spins up a full PoolService HTTP stack with admin
// auth and returns an authenticated client plus the session cookie value.
func startPoolEditTestServer(t *testing.T, database *db.DB, jwtSecret []byte, stats server.PoolStatsProvider) (*httptest.Server, supervisorv1connect.PoolServiceClient, string) {
	t.Helper()
	ctx := context.Background()

	srv := server.New(server.Options{
		Port:             8080,
		AuthDB:           database,
		PoolDB:           database,
		PoolStats:        stats,
		JWTSigningSecret: jwtSecret,
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
	return ts, supervisorv1connect.NewPoolServiceClient(ts.Client(), ts.URL), rawCookie
}

// editPoolPayload returns a complete, valid update payload for poolID with
// mutable fields the test phases adjust individually.
func editPoolPayload(poolID, profileID int64, name string) *supervisorv1.Pool {
	return &supervisorv1.Pool{
		Id:                       poolID,
		Name:                     name,
		Provider:                 "github",
		RepositoryUrl:            "https://github.com/acme/widgets",
		TargetUrls:               []string{"https://github.com/acme/widgets", "https://github.com/acme/gadgets"},
		Scope:                    "repo",
		AuthProfileId:            profileID,
		MinIdleRunners:           1,
		MaxConcurrency:           4,
		Labels:                   []string{"self-hosted", "linux"},
		RunnerImage:              "ghcr.io/noosxe/runnero:v1",
		AllowDocker:              true,
		MaxRunnerLifetimeSeconds: 3600,
		CpuLimit:                 "2",
		MemoryLimit:              "4g",
	}
}

func updatePool(t *testing.T, client supervisorv1connect.PoolServiceClient, rawCookie string, pool *supervisorv1.Pool) (*supervisorv1.Pool, error) {
	t.Helper()
	req := connect.NewRequest(&supervisorv1.UpdatePoolRequest{Pool: pool})
	req.Header().Set("Cookie", "session_token="+rawCookie)
	res, err := client.UpdatePool(context.Background(), req)
	if err != nil {
		return nil, err
	}
	return res.Msg.Pool, nil
}

func TestPoolServiceUpdatePoolEditSemantics(t *testing.T) {
	ctx := context.Background()
	database, jwtSecret := setupTestDB(t)
	stats := newMockStatsProvider()
	_, client, rawCookie := startPoolEditTestServer(t, database, jwtSecret, stats)

	profile, err := database.CreateAuthProfile(ctx, db.CreateAuthProfileParams{
		Name:           "edit-profile",
		AuthMethod:     "pat",
		TokenEncrypted: sql.NullString{String: "encrypted-token", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateAuthProfile failed: %v", err)
	}

	createReq := connect.NewRequest(&supervisorv1.CreatePoolRequest{
		Pool: editPoolPayload(0, profile.ID, "edit-pool"),
	})
	createReq.Header().Set("Cookie", "session_token="+rawCookie)
	createRes, err := client.CreatePool(ctx, createReq)
	if err != nil {
		t.Fatalf("CreatePool failed: %v", err)
	}
	poolID := createRes.Msg.Pool.Id

	// Second pool to collide names with later.
	dupReq := connect.NewRequest(&supervisorv1.CreatePoolRequest{
		Pool: &supervisorv1.Pool{
			Name:          "dup-pool",
			Provider:      "github",
			RepositoryUrl: "https://github.com/acme/other",
			Scope:         "repo",
			AuthProfileId: profile.ID,
			AllowDocker:   true,
		},
	})
	dupReq.Header().Set("Cookie", "session_token="+rawCookie)
	dupRes, err := client.CreatePool(ctx, dupReq)
	if err != nil {
		t.Fatalf("CreatePool (dup) failed: %v", err)
	}
	dupID := dupRes.Msg.Pool.Id

	targetIDsBefore := poolTargetIDs(t, database, poolID)

	// 1. Control-plane-only edit: no idle recycle, no target rewrite.
	control := editPoolPayload(poolID, profile.ID, "edit-pool")
	control.MinIdleRunners = 2
	control.MaxConcurrency = 9
	if _, err := updatePool(t, client, rawCookie, control); err != nil {
		t.Fatalf("control-plane update failed: %v", err)
	}
	if got := stats.recycledCount(poolID); got != 0 {
		t.Errorf("control-plane-only edit must not recycle idle runners, got %d recycles", got)
	}
	if got := poolTargetIDs(t, database, poolID); !targetIDsEqual(targetIDsBefore, got) {
		t.Errorf("unchanged target set must not rewrite pool_targets: before=%v after=%v", targetIDsBefore, got)
	}

	// 2. Spawn-identity edit (labels): recycles idle runners.
	identity := editPoolPayload(poolID, profile.ID, "edit-pool")
	identity.Labels = []string{"self-hosted", "gpu"}
	if _, err := updatePool(t, client, rawCookie, identity); err != nil {
		t.Fatalf("spawn-identity update failed: %v", err)
	}
	if got := stats.recycledCount(poolID); got != 1 {
		t.Errorf("spawn-identity edit must recycle idle runners once, got %d recycles", got)
	}

	// 3. Provider is immutable.
	switchPool := editPoolPayload(poolID, profile.ID, "edit-pool")
	switchPool.Provider = "forgejo"
	if _, err := updatePool(t, client, rawCookie, switchPool); err == nil {
		t.Fatal("provider change must be rejected")
	} else if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("provider change: want CodeInvalidArgument, got %v", connect.CodeOf(err))
	}

	// 4. Duplicate name maps to CodeAlreadyExists.
	dup := editPoolPayload(dupID, profile.ID, "edit-pool")
	if _, err := updatePool(t, client, rawCookie, dup); err == nil {
		t.Fatal("duplicate pool name must be rejected")
	} else if connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Errorf("duplicate name: want CodeAlreadyExists, got %v", connect.CodeOf(err))
	}

	// 5. Unknown id maps to CodeNotFound.
	missing := editPoolPayload(9999, profile.ID, "ghost-pool")
	if _, err := updatePool(t, client, rawCookie, missing); err == nil {
		t.Fatal("unknown pool id must be rejected")
	} else if connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("unknown id: want CodeNotFound, got %v", connect.CodeOf(err))
	}

	// 6. Invalid renovate cron rejected before any write.
	rowBefore, err := database.GetRunnerPoolById(ctx, poolID)
	if err != nil {
		t.Fatalf("GetRunnerPoolById failed: %v", err)
	}
	badCron := editPoolPayload(poolID, profile.ID, "edit-pool")
	badCron.Renovate = &supervisorv1.RenovateConfig{Enabled: true, CronSchedule: "not-a-cron"}
	if _, err := updatePool(t, client, rawCookie, badCron); err == nil {
		t.Fatal("invalid renovate cron must be rejected")
	} else if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("invalid cron: want CodeInvalidArgument, got %v", connect.CodeOf(err))
	}
	rowAfter, err := database.GetRunnerPoolById(ctx, poolID)
	if err != nil {
		t.Fatalf("GetRunnerPoolById failed: %v", err)
	}
	if rowAfter.MaxConcurrency != rowBefore.MaxConcurrency || rowAfter.Name != rowBefore.Name {
		t.Errorf("rejected update must not modify the pool row: before=%+v after=%+v", rowBefore, rowAfter)
	}

	// 7. Rename is metadata-only: it succeeds even with busy runners and never
	// recycles — tracking is keyed by pool id, so live runners are unaffected
	// (RUN-126, docs/22 §5.4).
	stats.mu.Lock()
	stats.activeCounts[poolID] = 2
	stats.mu.Unlock()
	rename := editPoolPayload(poolID, profile.ID, "edit-pool-renamed")
	rename.Labels = identity.Labels // pure rename: spawn identity untouched
	renamedPool, err := updatePool(t, client, rawCookie, rename)
	if err != nil {
		t.Fatalf("rename with busy runners must succeed: %v", err)
	}
	if renamedPool.Name != "edit-pool-renamed" {
		t.Errorf("rename response name=%q, want edit-pool-renamed", renamedPool.Name)
	}
	if got := stats.recycledCount(poolID); got != 1 {
		t.Errorf("rename must not recycle runners (still only the identity edit), got %d recycles", got)
	}
	rowAfter, err = database.GetRunnerPoolById(ctx, poolID)
	if err != nil {
		t.Fatalf("GetRunnerPoolById failed: %v", err)
	}
	if rowAfter.Name != "edit-pool-renamed" {
		t.Errorf("rename must rewrite the pool row, name=%q", rowAfter.Name)
	}
	if _, err := database.GetRunnerPoolByName(ctx, "edit-pool"); err == nil {
		t.Error("old pool name must no longer resolve")
	}

	// 9. Audit log records before/after restricted to changed fields.
	auditLogs, err := database.ListAuditLogs(ctx, db.ListAuditLogsParams{Limit: 10, Offset: 0})
	if err != nil || len(auditLogs) == 0 {
		t.Fatalf("expected audit logs, got err=%v count=%d", err, len(auditLogs))
	}
	var details struct {
		Changes map[string]struct {
			Before any `json:"before"`
			After  any `json:"after"`
		} `json:"changes"`
	}
	for _, log := range auditLogs {
		if log.Action == "pool.update" && log.ResourceID.Valid && log.ResourceID.Int64 == poolID {
			if err := json.Unmarshal([]byte(log.Details.String), &details); err != nil {
				t.Fatalf("parsing pool.update audit details: %v", err)
			}
			break
		}
	}
	if _, ok := details.Changes["name"]; !ok {
		t.Errorf("rename audit must record a name change, got %v", details.Changes)
	} else if details.Changes["name"].Before != "edit-pool" || details.Changes["name"].After != "edit-pool-renamed" {
		t.Errorf("rename audit name change mismatch: %+v", details.Changes["name"])
	}
	if _, ok := details.Changes["provider"]; ok {
		t.Errorf("audit must be restricted to changed fields; unexpected provider entry in %v", details.Changes)
	}
}

func poolTargetIDs(t *testing.T, database *db.DB, poolID int64) []int64 {
	t.Helper()
	targets, err := database.ListPoolTargetsByPoolId(context.Background(), poolID)
	if err != nil {
		t.Fatalf("ListPoolTargetsByPoolId failed: %v", err)
	}
	ids := make([]int64, 0, len(targets))
	for _, target := range targets {
		ids = append(ids, target.ID)
	}
	return ids
}

func targetIDsEqual(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
