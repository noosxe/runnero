package orchestrator_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/noosxe/runnero/internal/db"
	"github.com/noosxe/runnero/internal/orchestrator"
	"github.com/noosxe/runnero/internal/provider"
	"github.com/noosxe/runnero/internal/server"
)

type mockPoolRepo struct {
	pools    []db.RunnerPool
	settings map[string]string
	err      error
}

func (m *mockPoolRepo) ListRunnerPools(ctx context.Context) ([]db.RunnerPool, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.pools, nil
}

func (m *mockPoolRepo) GetAppSetting(ctx context.Context, key string) (db.AppSetting, error) {
	if m.settings != nil {
		if val, ok := m.settings[key]; ok {
			return db.AppSetting{Key: key, Value: val}, nil
		}
	}
	return db.AppSetting{}, sql.ErrNoRows
}

type mockGitProviderResolver struct {
	providers map[int64]provider.GitProvider
	err       error
}

func (m *mockGitProviderResolver) ResolveProvider(ctx context.Context, authProfileID int64) (provider.GitProvider, error) {
	if m.err != nil {
		return nil, m.err
	}
	p, ok := m.providers[authProfileID]
	if !ok {
		return nil, errors.New("provider not found")
	}
	return p, nil
}

type mockGitProvider struct {
	tokensIssued  []string
	deregistered  []string
	validateErr   error
	tokenErr      error
	scalingMode   provider.ScalingMode
	queuedJobs    int
	pollErr       error
	pollCalls     int
	remoteRunners []provider.RemoteRunnerStatus
	listErr       error
	listCalls     int
	deregErr      error
	deregCalls    int
}

func (m *mockGitProvider) ListRunners(ctx context.Context, scope provider.RegistrationScope, targetURL string) ([]provider.RemoteRunnerStatus, error) {
	m.listCalls++
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.remoteRunners, nil
}

func (m *mockGitProvider) GetRegistrationToken(ctx context.Context, scope provider.RegistrationScope, targetURL string) (string, error) {
	if m.tokenErr != nil {
		return "", m.tokenErr
	}
	token := "reg-token-mock"
	m.tokensIssued = append(m.tokensIssued, token)
	return token, nil
}

func (m *mockGitProvider) DeregisterRunner(ctx context.Context, scope provider.RegistrationScope, targetURL, runnerName string) error {
	m.deregCalls++
	if m.deregErr != nil {
		return m.deregErr
	}
	m.deregistered = append(m.deregistered, runnerName)
	return nil
}

func (m *mockGitProvider) ValidateCredentials(ctx context.Context) error {
	return m.validateErr
}

func (m *mockGitProvider) ScalingMode() provider.ScalingMode {
	if m.scalingMode != "" {
		return m.scalingMode
	}
	return provider.ScalingWebhook
}

func (m *mockGitProvider) PollQueuedJobs(ctx context.Context, target provider.PollTarget) (int, error) {
	m.pollCalls++
	if m.pollErr != nil {
		return 0, m.pollErr
	}
	return m.queuedJobs, nil
}

func (m *mockGitProvider) DiscoverOrganizations(ctx context.Context) ([]provider.DiscoveredTarget, error) {
	return nil, nil
}

func (m *mockGitProvider) DiscoverRepositories(ctx context.Context) ([]provider.DiscoveredTarget, error) {
	return nil, nil
}

func TestPoolController_BootAndMinIdleProvisioning(t *testing.T) {
	ctx := context.Background()

	pool := db.RunnerPool{
		ID:             118,
		Name:           "ci-pool",
		Provider:       "github",
		RepositoryUrl:  "https://github.com/owner/repo",
		Scope:          "repo",
		AuthProfileID:  10,
		MinIdleRunners: 3,
		MaxConcurrency: 5,
		Labels:         `["self-hosted","linux","arm64"]`,
		RunnerImage:    "ghcr.io/noosxe/runnero:latest",
		AllowDocker:    true,
		CpuLimit:       sql.NullString{String: "2", Valid: true},
		MemoryLimit:    sql.NullString{String: "4g", Valid: true},
	}

	repo := &mockPoolRepo{pools: []db.RunnerPool{pool}}
	gitProv := &mockGitProvider{}
	resolver := &mockGitProviderResolver{
		providers: map[int64]provider.GitProvider{10: gitProv},
	}

	spawnCount := 0
	mockEngine := &orchestrator.MockContainerProvider{
		SpawnRunnerFn: func(ctx context.Context, config orchestrator.RunnerConfig) (string, error) {
			spawnCount++
			if config.PoolName != "ci-pool" {
				t.Errorf("expected pool ci-pool, got %q", config.PoolName)
			}
			hasToken := false
			hasEphemeral := false
			for _, e := range config.Env {
				if e == "RUNNER_TOKEN=reg-token-mock" {
					hasToken = true
				}
				if e == "RUNNER_EPHEMERAL=1" {
					hasEphemeral = true
				}
			}
			if !hasToken {
				t.Errorf("expected RUNNER_TOKEN injected, got %v", config.Env)
			}
			if !hasEphemeral {
				t.Errorf("expected RUNNER_EPHEMERAL=1, got %v", config.Env)
			}
			if config.CPULimit != "2" || config.MemoryLimit != "4g" {
				t.Errorf("unexpected limits: cpu=%s mem=%s", config.CPULimit, config.MemoryLimit)
			}
			return "container-mock-" + config.Name, nil
		},
	}

	reconciler := orchestrator.NewReconciler(mockEngine)
	ctrl := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:               repo,
		ContainerEngine:  mockEngine,
		ProviderResolver: resolver,
		Reconciler:       reconciler,
	})

	// Initial check before boot
	probe := ctrl.ReadinessCheck()
	if status := probe.Check(ctx); status != server.StatusFail {
		t.Errorf("expected StatusFail before boot, got %v", status)
	}

	// 1. Boot sequence
	if err := ctrl.Boot(ctx); err != nil {
		t.Fatalf("ctrl.Boot failed: %v", err)
	}

	if ctrl.State() != orchestrator.StateRunning {
		t.Fatalf("expected state StateRunning, got %v", ctrl.State())
	}
	if spawnCount != 3 {
		t.Fatalf("expected 3 runners spawned on boot to satisfy min_idle_runners=3, got %d", spawnCount)
	}

	// Readiness check should now pass
	if status := probe.Check(ctx); status != server.StatusOK {
		t.Errorf("expected StatusOK after boot, got %v", status)
	}

	tracked := reconciler.TrackedPoolRunners(118)
	if len(tracked) != 3 {
		t.Fatalf("expected 3 tracked runners in reconciler, got %d", len(tracked))
	}

	// 2. Acceptance: Maintain pool of N idle runners from empty state
	// Simulate 1 runner exiting (finished job)
	mockEngine.AuditRunnersFn = func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
		return []orchestrator.RunnerStatus{
			{PoolID: 118, ID: tracked[0].ID, PoolName: "ci-pool", State: "running"},
			{PoolID: 118, ID: tracked[1].ID, PoolName: "ci-pool", State: "running"},
			{PoolID: 118, ID: tracked[2].ID, PoolName: "ci-pool", State: "exited"}, // Exited!
		}, nil
	}

	// Run Reconcile: should detect only 2 running and spawn 1 replacement
	if err := ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("ctrl.Reconcile failed: %v", err)
	}

	if spawnCount != 4 {
		t.Fatalf("expected 4 total spawns after 1 exited runner replaced, got %d", spawnCount)
	}

	// 3. Test Pause and Resume
	ctrl.Pause()
	if ctrl.State() != orchestrator.StatePaused {
		t.Fatalf("expected StatePaused, got %v", ctrl.State())
	}

	// Simulate another exit while paused
	mockEngine.AuditRunnersFn = func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
		return []orchestrator.RunnerStatus{
			{PoolID: 118, ID: tracked[0].ID, PoolName: "ci-pool", State: "running"},
		}, nil
	}

	// Reconcile while paused should not spawn anything
	if err := ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("ctrl.Reconcile while paused failed: %v", err)
	}
	if spawnCount != 4 {
		t.Errorf("spawning should not happen while paused, count=%d", spawnCount)
	}

	// Resume and Reconcile: should spawn replacements
	ctrl.Resume()
	if ctrl.State() != orchestrator.StateRunning {
		t.Fatalf("expected StateRunning after resume, got %v", ctrl.State())
	}

	if err := ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("ctrl.Reconcile after resume failed: %v", err)
	}
	if spawnCount <= 4 {
		t.Errorf("expected new spawns after resume, got %d", spawnCount)
	}
}

func TestPoolController_BootEngineUnreachable(t *testing.T) {
	ctx := context.Background()

	mockEngine := &orchestrator.MockContainerProvider{
		PingFn: func(ctx context.Context) error {
			return errors.New("cannot connect to docker.sock")
		},
	}

	ctrl := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		ContainerEngine: mockEngine,
	})

	err := ctrl.Boot(ctx)
	if err == nil || !errors.Is(err, orchestrator.ErrEngineUnreachable) {
		t.Fatalf("expected ErrEngineUnreachable, got %v", err)
	}
	if ctrl.State() != orchestrator.StateStopped {
		t.Errorf("expected StateStopped, got %v", ctrl.State())
	}
}

func TestPoolController_HandleContainerEvent_ReapAndReplenish(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()

	pool := db.RunnerPool{
		ID:             119,
		Name:           "event-pool",
		Provider:       "github",
		RepositoryUrl:  "https://github.com/owner/event-repo",
		Scope:          "repo",
		AuthProfileID:  20,
		MinIdleRunners: 2,
		RunnerImage:    "ghcr.io/noosxe/runnero:latest",
	}

	repo := &mockPoolRepo{pools: []db.RunnerPool{pool}}
	tokensFetched := 0
	gitProv := &mockGitProvider{}
	gitProv.tokensIssued = make([]string, 0)
	resolver := &mockGitProviderResolver{
		providers: map[int64]provider.GitProvider{20: gitProv},
	}

	terminatedIDs := make([]string, 0)
	logsCapturedIDs := make([]string, 0)
	spawnedIDs := make([]string, 0)

	mockEngine := &orchestrator.MockContainerProvider{
		SpawnRunnerFn: func(ctx context.Context, config orchestrator.RunnerConfig) (string, error) {
			tokensFetched++
			id := "runner-" + config.Name
			spawnedIDs = append(spawnedIDs, id)
			return id, nil
		},
		TerminateRunnerFn: func(ctx context.Context, containerID string) error {
			terminatedIDs = append(terminatedIDs, containerID)
			return nil
		},
		CaptureLogsFn: func(ctx context.Context, containerID, dataDir string) (string, error) {
			logsCapturedIDs = append(logsCapturedIDs, containerID)
			return orchestrator.LogPath(dataDir, containerID), nil
		},
	}

	reconciler := orchestrator.NewReconciler(mockEngine)
	ctrl := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:               repo,
		ContainerEngine:  mockEngine,
		ProviderResolver: resolver,
		Reconciler:       reconciler,
		DataDir:          tempDir,
	})

	// 1. Boot up: spawns 2 idle runners
	if err := ctrl.Boot(ctx); err != nil {
		t.Fatalf("ctrl.Boot failed: %v", err)
	}

	if len(spawnedIDs) != 2 {
		t.Fatalf("expected 2 spawned on boot, got %d", len(spawnedIDs))
	}
	firstRunnerID := spawnedIDs[0]

	// 2. Container completes job and dies -> "die" event arrives
	event := orchestrator.ContainerEvent{
		ContainerID: firstRunnerID,
		PoolName:    "event-pool",
		Action:      "die",
		ExitCode:    0,
	}

	// Handle event: must capture logs, terminate dead container, and immediately replenish
	if err := ctrl.HandleContainerEvent(ctx, event); err != nil {
		t.Fatalf("HandleContainerEvent failed: %v", err)
	}

	// Acceptance: dead container reaped, exit logs captured before prune
	if len(logsCapturedIDs) != 1 || logsCapturedIDs[0] != firstRunnerID {
		t.Errorf("expected logs captured for %s, got: %v", firstRunnerID, logsCapturedIDs)
	}
	if len(terminatedIDs) != 1 || terminatedIDs[0] != firstRunnerID {
		t.Errorf("expected container %s terminated, got: %v", firstRunnerID, terminatedIDs)
	}

	// Replacement runner spawned with fresh token, converging back to target 2 idle runners
	if len(spawnedIDs) != 3 {
		t.Fatalf("expected 3 total spawns (2 initial + 1 replacement), got %d", len(spawnedIDs))
	}
	if tokensFetched != 3 {
		t.Errorf("expected fresh token requested per spawn (3 total), got %d", tokensFetched)
	}

	tracked := reconciler.TrackedPoolRunners(119)
	if len(tracked) != 2 {
		t.Fatalf("expected 2 active runners in pool, got %d", len(tracked))
	}
}

// RUN-121: die and destroy events (and the audit cycle) can re-process the
// same container after another reap path already captured the logs and
// removed it. The duplicate reap must complete without error, treating the
// engine's benign "already gone / removal in progress" responses as success
// instead of warning, and must not double-replenish the pool.
func TestPoolController_HandleContainerEvent_DoubleDeliveryBenign(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()

	pool := db.RunnerPool{
		ID:             120,
		Name:           "double-delivery-pool",
		Provider:       "github",
		RepositoryUrl:  "https://github.com/owner/double-repo",
		Scope:          "repo",
		AuthProfileID:  21,
		MinIdleRunners: 1,
		RunnerImage:    "ghcr.io/noosxe/runnero:latest",
	}

	repo := &mockPoolRepo{pools: []db.RunnerPool{pool}}
	gitProv := &mockGitProvider{tokensIssued: make([]string, 0)}
	resolver := &mockGitProviderResolver{
		providers: map[int64]provider.GitProvider{21: gitProv},
	}

	var captureCalls, terminateCalls int
	spawnedIDs := make([]string, 0)
	mockEngine := &orchestrator.MockContainerProvider{
		SpawnRunnerFn: func(ctx context.Context, config orchestrator.RunnerConfig) (string, error) {
			id := "runner-dd-" + config.Name
			spawnedIDs = append(spawnedIDs, id)
			return id, nil
		},
		TerminateRunnerFn: func(ctx context.Context, containerID string) error {
			terminateCalls++
			return nil
		},
		CaptureLogsFn: func(ctx context.Context, containerID, dataDir string) (string, error) {
			captureCalls++
			if captureCalls > 1 {
				// Losing race: the winning reap path already captured the logs
				// and started removal (RUN-121).
				return "", orchestrator.ErrLogsUnavailable
			}
			return orchestrator.LogPath(dataDir, containerID), nil
		},
	}

	reconciler := orchestrator.NewReconciler(mockEngine)
	ctrl := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:               repo,
		ContainerEngine:  mockEngine,
		ProviderResolver: resolver,
		Reconciler:       reconciler,
		DataDir:          tempDir,
	})

	// 1. Boot: spawns 1 idle runner
	if err := ctrl.Boot(ctx); err != nil {
		t.Fatalf("ctrl.Boot failed: %v", err)
	}
	if len(spawnedIDs) != 1 {
		t.Fatalf("expected 1 spawned on boot, got %d", len(spawnedIDs))
	}
	firstRunnerID := spawnedIDs[0]

	// 2. Die event: the winning reap path captures logs and removes the container
	dieEvent := orchestrator.ContainerEvent{
		ContainerID: firstRunnerID,
		PoolName:    "double-delivery-pool",
		Action:      "die",
		ExitCode:    0,
	}
	if err := ctrl.HandleContainerEvent(ctx, dieEvent); err != nil {
		t.Fatalf("HandleContainerEvent(die) failed: %v", err)
	}
	if len(spawnedIDs) != 2 {
		t.Fatalf("expected 2 spawns (1 initial + 1 replacement), got %d", len(spawnedIDs))
	}

	// 3. Duplicate delivery: destroy event for the same container arrives
	// after removal already finished. Must be benign — no error, no WARN-worthy
	// engine failure, and no double replenish.
	destroyEvent := orchestrator.ContainerEvent{
		ContainerID: firstRunnerID,
		PoolName:    "double-delivery-pool",
		Action:      "destroy",
	}
	if err := ctrl.HandleContainerEvent(ctx, destroyEvent); err != nil {
		t.Fatalf("HandleContainerEvent(destroy) must treat duplicate delivery as benign, got: %v", err)
	}

	if len(spawnedIDs) != 2 {
		t.Errorf("expected no extra spawn on duplicate delivery, got %d spawns", len(spawnedIDs))
	}
	if captureCalls != 2 || terminateCalls != 2 {
		t.Errorf("expected reap attempted twice (capture=%d, terminate=%d), got capture=%d terminate=%d", 2, 2, captureCalls, terminateCalls)
	}
}
func TestPoolController_GlobalQuotaSaturationAndFairQueueDrain(t *testing.T) {
	ctx := context.Background()

	poolA := db.RunnerPool{
		ID:             100,
		Name:           "pool-a",
		Provider:       "github",
		RepositoryUrl:  "https://github.com/owner/repo-a",
		Scope:          "repo",
		AuthProfileID:  10,
		MinIdleRunners: 2,
		MaxConcurrency: 2,
		RunnerImage:    "ghcr.io/noosxe/runnero:latest",
	}
	poolB := db.RunnerPool{
		ID:             101,
		Name:           "pool-b",
		Provider:       "github",
		RepositoryUrl:  "https://github.com/owner/repo-b",
		Scope:          "repo",
		AuthProfileID:  10,
		MinIdleRunners: 2,
		MaxConcurrency: 2,
		RunnerImage:    "ghcr.io/noosxe/runnero:latest",
	}

	repo := &mockPoolRepo{pools: []db.RunnerPool{poolA, poolB}}
	gitProv := &mockGitProvider{}
	resolver := &mockGitProviderResolver{
		providers: map[int64]provider.GitProvider{10: gitProv},
	}

	spawnedByPool := make(map[string][]string)
	mockEngine := &orchestrator.MockContainerProvider{
		SpawnRunnerFn: func(ctx context.Context, config orchestrator.RunnerConfig) (string, error) {
			id := "runner-" + config.Name
			spawnedByPool[config.PoolName] = append(spawnedByPool[config.PoolName], id)
			return id, nil
		},
		TerminateRunnerFn: func(ctx context.Context, containerID string) error {
			return nil
		},
	}

	reconciler := orchestrator.NewReconciler(mockEngine)
	// Set GlobalMaxRunners = 3 (while poolA + poolB total target is 4)
	ctrl := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:               repo,
		ContainerEngine:  mockEngine,
		ProviderResolver: resolver,
		Reconciler:       reconciler,
		GlobalMaxRunners: 3,
	})

	// 1. Boot controller: should hit circuit breaker at 3 runners and queue the 4th request
	if err := ctrl.Boot(ctx); err != nil {
		t.Fatalf("ctrl.Boot failed: %v", err)
	}

	if ctrl.TotalActiveRunners() != 3 {
		t.Fatalf("expected exactly 3 total active runners (at circuit breaker limit), got %d", ctrl.TotalActiveRunners())
	}
	if ctrl.QueueLength() != 1 {
		t.Fatalf("expected 1 request queued for pool-b due to saturation, got %d", ctrl.QueueLength())
	}
	if ctrl.QueueLengthForPool(101) != 1 {
		t.Errorf("expected queued request to be for pool-b")
	}

	// Verify poolA has 2 and poolB has 1 active runner
	if len(spawnedByPool["pool-a"]) != 2 {
		t.Errorf("expected 2 spawns for pool-a, got %d", len(spawnedByPool["pool-a"]))
	}
	if len(spawnedByPool["pool-b"]) != 1 {
		t.Errorf("expected 1 spawn for pool-b, got %d", len(spawnedByPool["pool-b"]))
	}

	// 2. Terminate a container in pool-a
	runnerA1 := spawnedByPool["pool-a"][0]
	event := orchestrator.ContainerEvent{
		ContainerID: runnerA1,
		PoolName:    "pool-a",
		Action:      "die",
		ExitCode:    0,
	}

	// When container terminates, capacity frees up -> queue drains pool-b request
	// Since pool-b takes the freed slot (reaching 3 active), pool-a's replenishment is queued
	if err := ctrl.HandleContainerEvent(ctx, event); err != nil {
		t.Fatalf("HandleContainerEvent failed: %v", err)
	}

	// Queue for pool-b should now be drained
	if ctrl.QueueLengthForPool(101) != 0 {
		t.Errorf("expected pool-b queue to be drained after capacity freed, got length %d", ctrl.QueueLengthForPool(101))
	}

	// Total active runners must still never exceed GlobalMaxRunners (3)
	if ctrl.TotalActiveRunners() != 3 {
		t.Fatalf("expected total active runners to stay at global limit 3: got %d", ctrl.TotalActiveRunners())
	}

	// Pool B reached its target of 2 idle runners from queue drain
	if len(spawnedByPool["pool-b"]) != 2 {
		t.Errorf("expected pool-b to have received its 2nd runner from queue drain, got %d", len(spawnedByPool["pool-b"]))
	}

	// Pool A's replenishment request is now queued because capacity is at 3/3
	if ctrl.QueueLengthForPool(100) != 1 {
		t.Errorf("expected pool-a replenishment to be queued, got %d", ctrl.QueueLengthForPool(100))
	}

	// 3. Now terminate a container in pool-b to free capacity for pool-a's queued request
	runnerB1 := spawnedByPool["pool-b"][0]
	eventB := orchestrator.ContainerEvent{
		ContainerID: runnerB1,
		PoolName:    "pool-b",
		Action:      "die",
		ExitCode:    0,
	}

	if err := ctrl.HandleContainerEvent(ctx, eventB); err != nil {
		t.Fatalf("HandleContainerEvent for pool-b failed: %v", err)
	}

	// Now pool-a's queued request should have drained and spawned!
	if ctrl.QueueLengthForPool(100) != 0 {
		t.Errorf("expected pool-a queue to be drained, got %d", ctrl.QueueLengthForPool(100))
	}
	if ctrl.TotalActiveRunners() != 3 {
		t.Fatalf("expected total active runners to remain at global limit 3, got %d", ctrl.TotalActiveRunners())
	}
}

type mockJobRecorder struct {
	records []struct {
		poolID     int64
		runnerName string
		status     string
		logPath    string
		startedAt  time.Time
	}
	opens []struct {
		poolID     int64
		runnerName string
	}
	closes []struct {
		poolID     int64
		runnerName string
		status     string
		jobID      int64
	}
	interrupted int64

	webhookQueued    []webhookQueuedCall
	webhookStarted   []webhookStartedCall
	webhookCompleted []webhookCompletedCall

	failWebhook error
}

type webhookQueuedCall struct {
	poolID   int64
	jobID    int64
	meta     db.WebhookJobMeta
	queuedAt time.Time
}

type webhookStartedCall struct {
	poolID     int64
	jobID      int64
	runnerName string
	startedAt  time.Time
	queuedAt   time.Time
	meta       db.WebhookJobMeta
}

type webhookCompletedCall struct {
	poolID      int64
	jobID       int64
	runnerName  string
	status      string
	completedAt time.Time
}

func (m *mockJobRecorder) RecordJobTimeout(ctx context.Context, poolID int64, runnerName, logPath string, startedAt, completedAt time.Time) error {
	m.records = append(m.records, struct {
		poolID     int64
		runnerName string
		status     string
		logPath    string
		startedAt  time.Time
	}{
		poolID:     poolID,
		runnerName: runnerName,
		status:     "timeout",
		logPath:    logPath,
		startedAt:  startedAt,
	})
	return nil
}

func TestPoolController_HungRunnerAutoTermination(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()

	pool := db.RunnerPool{
		ID:                       120,
		Name:                     "timeout-pool",
		Provider:                 "github",
		RepositoryUrl:            "https://github.com/owner/timeout-repo",
		Scope:                    "repo",
		AuthProfileID:            10,
		MinIdleRunners:           2,
		MaxConcurrency:           5,
		MaxRunnerLifetimeSeconds: 5, // 5 second limit
		RunnerImage:              "ghcr.io/noosxe/runnero:latest",
	}

	repo := &mockPoolRepo{pools: []db.RunnerPool{pool}}
	gitProv := &mockGitProvider{}
	resolver := &mockGitProviderResolver{
		providers: map[int64]provider.GitProvider{10: gitProv},
	}

	terminatedIDs := make([]string, 0)
	logsCapturedIDs := make([]string, 0)
	mockEngine := &orchestrator.MockContainerProvider{
		SpawnRunnerFn: func(ctx context.Context, config orchestrator.RunnerConfig) (string, error) {
			return "runner-" + config.Name, nil
		},
		TerminateRunnerFn: func(ctx context.Context, containerID string) error {
			terminatedIDs = append(terminatedIDs, containerID)
			return nil
		},
		CaptureLogsFn: func(ctx context.Context, containerID, dataDir string) (string, error) {
			logsCapturedIDs = append(logsCapturedIDs, containerID)
			return orchestrator.LogPath(dataDir, containerID), nil
		},
	}

	reconciler := orchestrator.NewReconciler(mockEngine)
	jobRecorder := &mockJobRecorder{}

	ctrl := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:               repo,
		JobRecorder:      jobRecorder,
		ContainerEngine:  mockEngine,
		ProviderResolver: resolver,
		Reconciler:       reconciler,
		DataDir:          tempDir,
	})

	// Limit is 5s. Four runners exercise the docs/23 §8 matrix:
	// - busyAnchored: busy clock (BusySince) past the limit -> terminated,
	//   even though the busy anchor is *later* than spawn.
	// - busyFallback: busy with no anchor -> defensive spawn fallback
	//   (docs/23 §4.4.1) -> terminated on the spawn clock.
	// - idleStandby: idle well past the limit -> never lifetime-terminated
	//   (docs/23 §2: the churn fix).
	// - recentlyBusy: spawned past the limit but picked up its job 1s ago ->
	//   survives on the busy clock; under the old spawn-anchored semantics it
	//   would have been killed mid-job.
	now := time.Now().UTC()
	busyAnchored := orchestrator.RunnerStatus{
		PoolID:    120,
		ID:        "hung-container-1",
		Name:      "busy-anchored-1",
		PoolName:  "timeout-pool",
		State:     "running",
		IsBusy:    true,
		SpawnedAt: now.Add(-10 * time.Second),
		BusySince: now.Add(-6 * time.Second),
	}
	busyFallback := orchestrator.RunnerStatus{
		PoolID:    120,
		ID:        "hung-container-2",
		Name:      "busy-fallback-2",
		PoolName:  "timeout-pool",
		State:     "running",
		IsBusy:    true,
		SpawnedAt: now.Add(-7 * time.Second),
	}
	idleStandby := orchestrator.RunnerStatus{
		PoolID:    120,
		ID:        "old-standby-3",
		Name:      "old-standby-3",
		PoolName:  "timeout-pool",
		State:     "running",
		IsBusy:    false,
		SpawnedAt: now.Add(-10 * time.Second),
	}
	recentlyBusy := orchestrator.RunnerStatus{
		PoolID:    120,
		ID:        "recently-busy-4",
		Name:      "recently-busy-4",
		PoolName:  "timeout-pool",
		State:     "running",
		IsBusy:    true,
		SpawnedAt: now.Add(-10 * time.Second),
		BusySince: now.Add(-1 * time.Second),
	}
	reconciler.TrackRunner(busyAnchored)
	reconciler.TrackRunner(busyFallback)
	reconciler.TrackRunner(idleStandby)
	reconciler.TrackRunner(recentlyBusy)

	if len(reconciler.TrackedPoolRunners(120)) != 4 {
		t.Fatalf("expected 4 runners tracked initially")
	}

	// Trigger hung runner inspection
	if err := ctrl.CheckHungRunners(ctx); err != nil {
		t.Fatalf("CheckHungRunners failed: %v", err)
	}

	// 1. Acceptance: exactly the two over-limit busy runners are terminated.
	terminatedWant := map[string]bool{"hung-container-1": true, "hung-container-2": true}
	if len(terminatedIDs) != len(terminatedWant) {
		t.Fatalf("expected %d terminated runners, got: %v", len(terminatedWant), terminatedIDs)
	}
	for _, id := range terminatedIDs {
		if !terminatedWant[id] {
			t.Fatalf("unexpected termination of %q, got: %v", id, terminatedIDs)
		}
	}

	// 2. Logs captured before container termination for both kills.
	if len(logsCapturedIDs) != len(terminatedWant) {
		t.Errorf("expected logs captured for both hung runners, got: %v", logsCapturedIDs)
	}

	// 3. job_history: one 'timeout' row per kill, started_at anchored at the
	//   clock that fired the kill — BusySince for the anchored runner,
	//   SpawnedAt for the fallback runner (docs/23 §4.3).
	if len(jobRecorder.records) != 2 {
		t.Fatalf("expected 2 job_history records, got %d", len(jobRecorder.records))
	}
	for _, record := range jobRecorder.records {
		if record.poolID != 120 || record.status != "timeout" {
			t.Errorf("unexpected timeout record: %+v", record)
		}
		switch record.runnerName {
		case "busy-anchored-1":
			if !record.startedAt.Equal(busyAnchored.BusySince) {
				t.Errorf("anchored kill must record started_at=BusySince, got %v want %v", record.startedAt, busyAnchored.BusySince)
			}
		case "busy-fallback-2":
			if !record.startedAt.Equal(busyFallback.SpawnedAt) {
				t.Errorf("fallback kill must record started_at=SpawnedAt, got %v want %v", record.startedAt, busyFallback.SpawnedAt)
			}
		default:
			t.Errorf("unexpected timeout record for %q", record.runnerName)
		}
	}

	// 4. The idle standby and the recently-busy runner survive and remain
	//   tracked — the idle-standby churn fix and the busy-clock extension.
	tracked := reconciler.TrackedPoolRunners(120)
	if len(tracked) != 2 {
		t.Fatalf("expected 2 surviving runners, tracked: %+v", tracked)
	}
	surviving := make(map[string]orchestrator.RunnerStatus, len(tracked))
	for _, r := range tracked {
		surviving[r.ID] = r
	}
	if s, ok := surviving["old-standby-3"]; !ok || s.IsBusy {
		t.Errorf("idle standby past the limit must survive untouched, got: %+v", s)
	}
	if s, ok := surviving["recently-busy-4"]; !ok || !s.IsBusy {
		t.Errorf("runner spawned past the limit but busy for 1s must survive, got: %+v", s)
	}
}

// TestPoolController_HungRunnerCheck_DisabledWhenLifetimeZero regression-guards
// the docs/23 §5 config contract: max_runner_lifetime_seconds <= 0 disables the
// kill switch entirely — no termination, busy or idle.
func TestPoolController_HungRunnerCheck_DisabledWhenLifetimeZero(t *testing.T) {
	ctx := context.Background()
	pool := db.RunnerPool{
		ID:                       121,
		Name:                     "no-limit-pool",
		Provider:                 "github",
		RepositoryUrl:            "https://github.com/owner/no-limit-repo",
		Scope:                    "repo",
		AuthProfileID:            10,
		MinIdleRunners:           1,
		MaxConcurrency:           5,
		MaxRunnerLifetimeSeconds: 0,
		RunnerImage:              "ghcr.io/noosxe/runnero:latest",
	}
	repo := &mockPoolRepo{pools: []db.RunnerPool{pool}}
	gitProv := &mockGitProvider{}
	resolver := &mockGitProviderResolver{providers: map[int64]provider.GitProvider{10: gitProv}}

	terminated := make([]string, 0)
	mockEngine := &orchestrator.MockContainerProvider{
		TerminateRunnerFn: func(ctx context.Context, containerID string) error {
			terminated = append(terminated, containerID)
			return nil
		},
		PingFn: func(ctx context.Context) error { return nil },
	}
	reconciler := orchestrator.NewReconciler(mockEngine)
	ctrl := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:               repo,
		ContainerEngine:  mockEngine,
		ProviderResolver: resolver,
		Reconciler:       reconciler,
	})

	now := time.Now().UTC()
	for _, r := range []orchestrator.RunnerStatus{
		{PoolID: 121, ID: "busy-old", Name: "busy-old", PoolName: pool.Name, State: "running", IsBusy: true, SpawnedAt: now.Add(-time.Hour), BusySince: now.Add(-time.Hour)},
		{PoolID: 121, ID: "idle-old", Name: "idle-old", PoolName: pool.Name, State: "running", IsBusy: false, SpawnedAt: now.Add(-time.Hour)},
	} {
		reconciler.TrackRunner(r)
	}

	if err := ctrl.CheckHungRunners(ctx); err != nil {
		t.Fatalf("CheckHungRunners failed: %v", err)
	}
	if len(terminated) != 0 {
		t.Fatalf("lifetime=0 must disable the kill switch, terminated: %v", terminated)
	}
	if got := len(reconciler.TrackedPoolRunners(121)); got != 2 {
		t.Fatalf("both runners must remain tracked, got %d", got)
	}
}

func TestPoolController_GracefulShutdown_SIGTERM(t *testing.T) {
	ctx := context.Background()

	pool := db.RunnerPool{
		ID:            117,
		Name:          "shutdown-pool",
		Provider:      "github",
		RepositoryUrl: "https://github.com/owner/shutdown-repo",
		Scope:         "repo",
		AuthProfileID: 1,
	}

	repo := &mockPoolRepo{pools: []db.RunnerPool{pool}}
	gitProv := &mockGitProvider{}
	resolver := &mockGitProviderResolver{
		providers: map[int64]provider.GitProvider{1: gitProv},
	}

	terminated := make([]string, 0)
	mockEngine := &orchestrator.MockContainerProvider{
		TerminateRunnerFn: func(ctx context.Context, containerID string) error {
			terminated = append(terminated, containerID)
			return nil
		},
	}

	reconciler := orchestrator.NewReconciler(mockEngine)
	ctrl := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:                   repo,
		ContainerEngine:      mockEngine,
		ProviderResolver:     resolver,
		Reconciler:           reconciler,
		ShutdownTimeout:      1 * time.Second,
		ShutdownPollInterval: 20 * time.Millisecond,
	})

	// Setup: 1 idle runner and 1 busy runner
	idleRunner := orchestrator.RunnerStatus{
		PoolID:    117,
		ID:        "idle-runner-1",
		Name:      "idle-1",
		PoolName:  "shutdown-pool",
		State:     "running",
		IsBusy:    false,
		SpawnedAt: time.Now().UTC(),
	}
	busyRunner := orchestrator.RunnerStatus{
		PoolID:    117,
		ID:        "busy-runner-2",
		Name:      "busy-2",
		PoolName:  "shutdown-pool",
		State:     "running",
		IsBusy:    true,
		SpawnedAt: time.Now().UTC(),
	}

	reconciler.TrackRunner(idleRunner)
	reconciler.TrackRunner(busyRunner)

	auditCalls := 0
	mockEngine.AuditRunnersFn = func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
		auditCalls++
		if auditCalls == 1 {
			// First audit: runner is still running
			return []orchestrator.RunnerStatus{busyRunner}, nil
		}
		// Subsequent audit: runner has exited (job finished)
		return []orchestrator.RunnerStatus{
			{PoolID: 117, ID: "busy-runner-2", Name: "busy-2", PoolName: "shutdown-pool", State: "exited"},
		}, nil
	}

	// Execute SIGTERM graceful shutdown
	err := ctrl.GracefulShutdown(ctx)
	if err != nil {
		t.Fatalf("GracefulShutdown failed: %v", err)
	}

	// 1. Controller state must be StateStopped
	if ctrl.State() != orchestrator.StateStopped {
		t.Errorf("expected StateStopped, got %v", ctrl.State())
	}

	// 2. Idle runner terminated immediately
	idleTerminated := false
	for _, id := range terminated {
		if id == "idle-runner-1" {
			idleTerminated = true
		}
	}
	if !idleTerminated {
		t.Errorf("expected idle runner to be terminated immediately")
	}

	// 3. Busy runner allowed to complete and then reaped
	busyTerminated := false
	for _, id := range terminated {
		if id == "busy-runner-2" {
			busyTerminated = true
		}
	}
	if !busyTerminated {
		t.Errorf("expected busy runner to be reaped on job completion")
	}

	// 4. Zero ghost registrations: provider deregistration called for runners
	if len(gitProv.deregistered) == 0 {
		t.Errorf("expected provider DeregisterRunner to be called")
	}
}

func TestPoolController_GracefulShutdown_TimeoutExceeded(t *testing.T) {
	ctx := context.Background()

	pool := db.RunnerPool{
		ID:            121,
		Name:          "timeout-shutdown-pool",
		Provider:      "github",
		RepositoryUrl: "https://github.com/owner/timeout-repo",
		Scope:         "repo",
		AuthProfileID: 1,
	}

	repo := &mockPoolRepo{pools: []db.RunnerPool{pool}}
	gitProv := &mockGitProvider{}
	resolver := &mockGitProviderResolver{
		providers: map[int64]provider.GitProvider{1: gitProv},
	}

	terminated := make([]string, 0)
	mockEngine := &orchestrator.MockContainerProvider{
		TerminateRunnerFn: func(ctx context.Context, containerID string) error {
			terminated = append(terminated, containerID)
			return nil
		},
	}

	reconciler := orchestrator.NewReconciler(mockEngine)
	ctrl := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:                   repo,
		ContainerEngine:      mockEngine,
		ProviderResolver:     resolver,
		Reconciler:           reconciler,
		ShutdownTimeout:      50 * time.Millisecond, // Short timeout
		ShutdownPollInterval: 10 * time.Millisecond,
	})

	busyRunner := orchestrator.RunnerStatus{
		PoolID:    121,
		ID:        "busy-runner-stuck",
		Name:      "busy-stuck",
		PoolName:  "timeout-shutdown-pool",
		State:     "running",
		IsBusy:    true,
		SpawnedAt: time.Now().UTC(),
	}
	reconciler.TrackRunner(busyRunner)

	// Container never exits, remains running
	mockEngine.AuditRunnersFn = func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
		return []orchestrator.RunnerStatus{busyRunner}, nil
	}

	// Graceful shutdown should wait up to 50ms and then force terminate
	if err := ctrl.GracefulShutdown(ctx); err != nil {
		t.Fatalf("GracefulShutdown failed: %v", err)
	}

	if ctrl.State() != orchestrator.StateStopped {
		t.Errorf("expected StateStopped, got %v", ctrl.State())
	}

	// Container must be force-terminated
	forceTerminated := false
	for _, id := range terminated {
		if id == "busy-runner-stuck" {
			forceTerminated = true
		}
	}
	if !forceTerminated {
		t.Errorf("expected stuck busy runner to be force terminated after timeout")
	}
}

func TestPoolController_ImmediateShutdown_SIGINT(t *testing.T) {
	ctx := context.Background()

	pool := db.RunnerPool{
		ID:            122,
		Name:          "immediate-pool",
		Provider:      "github",
		RepositoryUrl: "https://github.com/owner/immediate-repo",
		Scope:         "repo",
		AuthProfileID: 1,
	}

	repo := &mockPoolRepo{pools: []db.RunnerPool{pool}}
	gitProv := &mockGitProvider{}
	resolver := &mockGitProviderResolver{
		providers: map[int64]provider.GitProvider{1: gitProv},
	}

	terminated := make([]string, 0)
	mockEngine := &orchestrator.MockContainerProvider{
		TerminateRunnerFn: func(ctx context.Context, containerID string) error {
			terminated = append(terminated, containerID)
			return nil
		},
	}

	reconciler := orchestrator.NewReconciler(mockEngine)
	ctrl := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:               repo,
		ContainerEngine:  mockEngine,
		ProviderResolver: resolver,
		Reconciler:       reconciler,
	})

	r1 := orchestrator.RunnerStatus{
		PoolID: 122, ID: "runner-idle", Name: "r-idle", PoolName: "immediate-pool", State: "running", IsBusy: false}
	r2 := orchestrator.RunnerStatus{
		PoolID: 122, ID: "runner-busy", Name: "r-busy", PoolName: "immediate-pool", State: "running", IsBusy: true}
	reconciler.TrackRunner(r1)
	reconciler.TrackRunner(r2)

	// Immediate shutdown: all containers terminated immediately
	if err := ctrl.ImmediateShutdown(ctx); err != nil {
		t.Fatalf("ImmediateShutdown failed: %v", err)
	}

	if ctrl.State() != orchestrator.StateStopped {
		t.Errorf("expected StateStopped, got %v", ctrl.State())
	}

	if len(terminated) != 2 {
		t.Fatalf("expected both runners terminated immediately, got %d", len(terminated))
	}

	// Deregistered called for both
	if len(gitProv.deregistered) != 2 {
		t.Errorf("expected both runners deregistered with provider, got %d", len(gitProv.deregistered))
	}
}

func TestPoolController_PerPoolSettingsRuntimeReload(t *testing.T) {
	ctx := context.Background()

	poolA := db.RunnerPool{
		ID:             100,
		Name:           "pool-a",
		Provider:       "github",
		RepositoryUrl:  "https://github.com/owner/repo-a",
		Scope:          "repo",
		AuthProfileID:  1,
		MinIdleRunners: 1,
		MaxConcurrency: 5,
		RunnerImage:    "ghcr.io/noosxe/runnero:latest",
	}

	repo := &mockPoolRepo{
		pools: []db.RunnerPool{poolA},
		settings: map[string]string{
			"total_allowed_runners": "10",
		},
	}
	gitProv := &mockGitProvider{}
	resolver := &mockGitProviderResolver{
		providers: map[int64]provider.GitProvider{1: gitProv},
	}

	spawnCounter := 0
	terminated := make([]string, 0)
	var reconciler *orchestrator.Reconciler
	mockEngine := &orchestrator.MockContainerProvider{
		SpawnRunnerFn: func(ctx context.Context, config orchestrator.RunnerConfig) (string, error) {
			spawnCounter++
			return fmt.Sprintf("%s-c%d", config.PoolName, spawnCounter), nil
		},
		TerminateRunnerFn: func(ctx context.Context, containerID string) error {
			terminated = append(terminated, containerID)
			return nil
		},
		AuditRunnersFn: func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
			if reconciler == nil {
				return nil, nil
			}
			return reconciler.TrackedPoolRunners(100), nil
		},
	}

	reconciler = orchestrator.NewReconciler(mockEngine)
	ctrl := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:               repo,
		ContainerEngine:  mockEngine,
		ProviderResolver: resolver,
		Reconciler:       reconciler,
	})

	if err := ctrl.Boot(ctx); err != nil {
		t.Fatalf("Boot failed: %v", err)
	}

	// 1. Initial boot: min_idle=1 -> 1 container provisioned
	if ctrl.TotalActiveRunners() != 1 {
		t.Fatalf("expected 1 active runner initially, got %d", ctrl.TotalActiveRunners())
	}

	// 2. Acceptance: Edit min_idle live (1 -> 3) -> pool converges upwards to 3
	repo.pools[0].MinIdleRunners = 3
	if err := ctrl.Reload(ctx); err != nil {
		t.Fatalf("Reload after increasing min_idle failed: %v", err)
	}

	if ctrl.TotalActiveRunners() != 3 {
		t.Fatalf("expected 3 active runners after scaling up, got %d", ctrl.TotalActiveRunners())
	}

	// 3. Acceptance: Edit min_idle live (3 -> 1) -> pool converges downwards to 1
	repo.pools[0].MinIdleRunners = 1
	if err := ctrl.Reload(ctx); err != nil {
		t.Fatalf("Reload after decreasing min_idle failed: %v", err)
	}

	if ctrl.TotalActiveRunners() != 1 {
		t.Fatalf("expected 1 active runner after scaling down, got %d", ctrl.TotalActiveRunners())
	}
	if len(terminated) != 2 {
		t.Errorf("expected 2 excess idle runners to be terminated during scale down, got %d", len(terminated))
	}

	// 4. Acceptance: Edit max_concurrency live (capped at 2 while min_idle is 4) -> converges to 2
	repo.pools[0].MinIdleRunners = 4
	repo.pools[0].MaxConcurrency = 2
	if err := ctrl.Reload(ctx); err != nil {
		t.Fatalf("Reload after setting max_concurrency failed: %v", err)
	}

	if ctrl.TotalActiveRunners() != 2 {
		t.Fatalf("expected active runners capped at max_concurrency=2, got %d", ctrl.TotalActiveRunners())
	}

	// 5. Acceptance: Remove pool from DB -> all its runners drained
	repo.pools = []db.RunnerPool{} // pool deleted from DB
	if err := ctrl.Reload(ctx); err != nil {
		t.Fatalf("Reload after deleting pool failed: %v", err)
	}

	if ctrl.TotalActiveRunners() != 0 {
		t.Fatalf("expected 0 active runners after pool deletion, got %d", ctrl.TotalActiveRunners())
	}
}

type mockTaskExitHandler struct {
	handledIDs []string
	exitCodes  []int
}

func (m *mockTaskExitHandler) HandleContainerExit(ctx context.Context, containerID string, exitCode int, logPath string) (bool, error) {
	if strings.HasPrefix(containerID, "renovate-task-") {
		m.handledIDs = append(m.handledIDs, containerID)
		m.exitCodes = append(m.exitCodes, exitCode)
		return true, nil
	}
	return false, nil
}

func TestPoolController_TaskExitHandlerReap(t *testing.T) {
	ctx := context.Background()
	var terminated []string
	engine := &orchestrator.MockContainerProvider{
		TerminateRunnerFn: func(ctx context.Context, containerID string) error {
			terminated = append(terminated, containerID)
			return nil
		},
	}
	taskHandler := &mockTaskExitHandler{}

	mockDB := &mockPoolRepo{pools: []db.RunnerPool{}}
	ctrl := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:              mockDB,
		ContainerEngine: engine,
		TaskExitHandler: taskHandler,
	})
	if err := ctrl.Boot(ctx); err != nil {
		t.Fatalf("ctrl.Boot failed: %v", err)
	}

	// 1. Task container exit event
	evt := orchestrator.ContainerEvent{
		ContainerID: "renovate-task-cid-1",
		PoolName:    "prod-pool",
		Action:      "die",
		ExitCode:    0,
	}

	if err := ctrl.HandleContainerEvent(ctx, evt); err != nil {
		t.Fatalf("HandleContainerEvent failed: %v", err)
	}

	if len(taskHandler.handledIDs) != 1 || taskHandler.handledIDs[0] != "renovate-task-cid-1" {
		t.Fatalf("expected task handler to handle renovate container, got %v", taskHandler.handledIDs)
	}

	if len(terminated) != 1 || terminated[0] != "renovate-task-cid-1" {
		t.Fatalf("expected task container to be terminated/reaped, got %v", terminated)
	}
}

func TestPoolController_ImageUpdateHandoff_Replenisher(t *testing.T) {
	ctx := context.Background()
	oldImage := "ghcr.io/noosxe/runnero:v1.0.0"
	newImage := "ghcr.io/noosxe/runnero:v2.0.0"

	pool := db.RunnerPool{
		ID:             124,
		Name:           "handoff-pool",
		Provider:       "github",
		RepositoryUrl:  "https://github.com/owner/repo",
		Scope:          "repo",
		AuthProfileID:  10,
		MinIdleRunners: 1,
		MaxConcurrency: 2,
		RunnerImage:    oldImage,
	}

	repo := &mockPoolRepo{pools: []db.RunnerPool{pool}}
	gitProv := &mockGitProvider{}
	resolver := &mockGitProviderResolver{
		providers: map[int64]provider.GitProvider{10: gitProv},
	}

	var spawnedConfigs []orchestrator.RunnerConfig
	var terminatedIDs []string
	var spawnIdx int

	mockEngine := &orchestrator.MockContainerProvider{
		SpawnRunnerFn: func(ctx context.Context, config orchestrator.RunnerConfig) (string, error) {
			spawnIdx++
			cid := fmt.Sprintf("cnt-runner-%d", spawnIdx)
			spawnedConfigs = append(spawnedConfigs, config)
			return cid, nil
		},
		TerminateRunnerFn: func(ctx context.Context, containerID string) error {
			terminatedIDs = append(terminatedIDs, containerID)
			return nil
		},
	}

	reconciler := orchestrator.NewReconciler(mockEngine)
	ctrl := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:               repo,
		ContainerEngine:  mockEngine,
		ProviderResolver: resolver,
		Reconciler:       reconciler,
		Interval:         100 * time.Millisecond,
		GlobalMaxRunners: 10,
		DataDir:          t.TempDir(),
	})

	if err := ctrl.Boot(ctx); err != nil {
		t.Fatalf("ctrl.Boot failed: %v", err)
	}

	// 1. Initial idle runner spawned with oldImage
	if len(spawnedConfigs) != 1 {
		t.Fatalf("expected 1 runner spawned at boot, got %d", len(spawnedConfigs))
	}
	if spawnedConfigs[0].Image != oldImage {
		t.Fatalf("expected initial runner to use %s, got %s", oldImage, spawnedConfigs[0].Image)
	}
	runner1CID := "cnt-runner-1"

	// 2. Runner 1 picks up a job (in-flight)
	reconciler.TrackRunner(orchestrator.RunnerStatus{
		PoolID:    124,
		ID:        runner1CID,
		PoolName:  "handoff-pool",
		State:     "running",
		IsBusy:    true,
		SpawnedAt: time.Now().UTC(),
	})

	// 3. Image update occurs: pool image is updated to newImage (RUN-67)
	repo.pools[0].RunnerImage = newImage

	// 4. Replenisher runs to maintain min-idle capacity
	if err := ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("ctrl.Reconcile failed: %v", err)
	}

	// Active runner 1 on old image MUST NOT be terminated while job is in-flight!
	for _, termID := range terminatedIDs {
		if termID == runner1CID {
			t.Fatalf("runner 1 was prematurely terminated while in-flight!")
		}
	}

	// Replenisher provisions runner 2 using newImage
	if len(spawnedConfigs) != 2 {
		t.Fatalf("expected 2 runners spawned, got %d", len(spawnedConfigs))
	}
	if spawnedConfigs[1].Image != newImage {
		t.Errorf("expected newly replenished runner to use %s, got %s", newImage, spawnedConfigs[1].Image)
	}

	// 5. In-flight job on runner 1 completes: container exits and is reaped
	exitEvt := orchestrator.ContainerEvent{
		ContainerID: runner1CID,
		PoolName:    "handoff-pool",
		Action:      "die",
		ExitCode:    0,
	}
	if err := ctrl.HandleContainerEvent(ctx, exitEvt); err != nil {
		t.Fatalf("HandleContainerEvent failed: %v", err)
	}

	// Runner 1 is reaped
	foundReaped := false
	for _, termID := range terminatedIDs {
		if termID == runner1CID {
			foundReaped = true
			break
		}
	}
	if !foundReaped {
		t.Errorf("expected runner 1 to be reaped after exit")
	}

	// Verify all spawned runners after update used newImage
	for i := 1; i < len(spawnedConfigs); i++ {
		if spawnedConfigs[i].Image != newImage {
			t.Errorf("spawned runner %d expected image %s, got %s", i, newImage, spawnedConfigs[i].Image)
		}
	}
}

func TestPoolController_ForgejoPollingScaling_AuditLoopPicksUpQueuedJob(t *testing.T) {
	ctx := context.Background()

	pool := db.RunnerPool{
		ID:             125,
		Name:           "forgejo-ci",
		Provider:       "forgejo",
		RepositoryUrl:  "https://forgejo.example.com/owner/repo",
		Scope:          "repo",
		AuthProfileID:  10,
		MinIdleRunners: 1,
		MaxConcurrency: 5,
		Labels:         `["self-hosted","linux"]`,
		RunnerImage:    "ghcr.io/noosxe/runnero:latest",
	}

	repo := &mockPoolRepo{pools: []db.RunnerPool{pool}}
	gitProv := &mockGitProvider{
		scalingMode: provider.ScalingPolling,
		queuedJobs:  0,
	}
	resolver := &mockGitProviderResolver{
		providers: map[int64]provider.GitProvider{10: gitProv},
	}

	spawnCounter := 0
	var reconciler1 *orchestrator.Reconciler
	mockEngine := &orchestrator.MockContainerProvider{
		SpawnRunnerFn: func(ctx context.Context, config orchestrator.RunnerConfig) (string, error) {
			spawnCounter++
			return fmt.Sprintf("forgejo-runner-%d", spawnCounter), nil
		},
		AuditRunnersFn: func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
			if reconciler1 == nil {
				return nil, nil
			}
			return reconciler1.TrackedPoolRunners(125), nil
		},
		PingFn: func(ctx context.Context) error {
			return nil
		},
	}
	reconciler1 = orchestrator.NewReconciler(mockEngine)

	ctrl := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:               repo,
		ContainerEngine:  mockEngine,
		ProviderResolver: resolver,
		Reconciler:       reconciler1,
		GlobalMaxRunners: 10,
		Interval:         time.Hour,
	})

	// 1. Boot provisions 1 base idle runner
	if err := ctrl.Boot(ctx); err != nil {
		t.Fatalf("Boot failed: %v", err)
	}
	if spawnCounter != 1 {
		t.Fatalf("expected 1 runner on boot, got %d", spawnCounter)
	}

	// 2. Forgejo has 3 queued jobs; idle runners is 1 -> deficit is 2
	gitProv.queuedJobs = 3

	// Audit loop reconciliation cycle runs
	if err := ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// Acceptance: queued jobs picked up within one audit cycle (2 additional runners provisioned)
	if spawnCounter != 3 {
		t.Fatalf("expected 3 total runners spawned (1 initial + 2 for queued jobs), got %d", spawnCounter)
	}
	if ctrl.TotalActiveRunners() != 3 {
		t.Fatalf("expected 3 active runners in pool, got %d", ctrl.TotalActiveRunners())
	}
	if gitProv.pollCalls < 1 {
		t.Errorf("expected PollQueuedJobs to be called during audit cycle, got %d calls", gitProv.pollCalls)
	}
}

func TestPoolController_ForgejoPollingScaling_MaxConcurrencyRespected(t *testing.T) {
	ctx := context.Background()

	pool := db.RunnerPool{
		ID:             126,
		Name:           "forgejo-capped",
		Provider:       "forgejo",
		RepositoryUrl:  "https://forgejo.example.com/owner/repo",
		Scope:          "repo",
		AuthProfileID:  10,
		MinIdleRunners: 1,
		MaxConcurrency: 3, // capped at 3
		Labels:         `["self-hosted","linux"]`,
		RunnerImage:    "ghcr.io/noosxe/runnero:latest",
	}

	repo := &mockPoolRepo{pools: []db.RunnerPool{pool}}
	gitProv := &mockGitProvider{
		scalingMode: provider.ScalingPolling,
		queuedJobs:  0,
	}
	resolver := &mockGitProviderResolver{
		providers: map[int64]provider.GitProvider{10: gitProv},
	}

	spawnCounter := 0
	var reconciler2 *orchestrator.Reconciler
	mockEngine := &orchestrator.MockContainerProvider{
		SpawnRunnerFn: func(ctx context.Context, config orchestrator.RunnerConfig) (string, error) {
			spawnCounter++
			return fmt.Sprintf("capped-runner-%d", spawnCounter), nil
		},
		AuditRunnersFn: func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
			if reconciler2 == nil {
				return nil, nil
			}
			return reconciler2.TrackedPoolRunners(126), nil
		},
		PingFn: func(ctx context.Context) error {
			return nil
		},
	}
	reconciler2 = orchestrator.NewReconciler(mockEngine)

	ctrl := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:               repo,
		ContainerEngine:  mockEngine,
		ProviderResolver: resolver,
		Reconciler:       reconciler2,
		GlobalMaxRunners: 10,
		Interval:         time.Hour,
	})

	if err := ctrl.Boot(ctx); err != nil {
		t.Fatalf("Boot failed: %v", err)
	}

	// 10 queued jobs, but max_concurrency is 3
	gitProv.queuedJobs = 10

	// Reconcile cycle detects 10 queued jobs -> provisions up to MaxConcurrency (3)
	if err := ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	if ctrl.TotalActiveRunners() > 3 {
		t.Fatalf("active runners %d exceeded max_concurrency 3", ctrl.TotalActiveRunners())
	}
	if ctrl.TotalActiveRunners() != 3 {
		t.Fatalf("expected exactly 3 runners (max_concurrency), got %d (total spawned %d)",
			ctrl.TotalActiveRunners(), spawnCounter)
	}
}

func TestPoolController_ForgejoPollingScaling_GlobalQuotaSaturation(t *testing.T) {
	ctx := context.Background()

	pool := db.RunnerPool{
		ID:             127,
		Name:           "forgejo-quota",
		Provider:       "forgejo",
		RepositoryUrl:  "https://forgejo.example.com/owner/repo",
		Scope:          "repo",
		AuthProfileID:  10,
		MinIdleRunners: 1,
		MaxConcurrency: 5,
		Labels:         `["self-hosted","linux"]`,
		RunnerImage:    "ghcr.io/noosxe/runnero:latest",
	}

	repo := &mockPoolRepo{pools: []db.RunnerPool{pool}}
	gitProv := &mockGitProvider{
		scalingMode: provider.ScalingPolling,
		queuedJobs:  0,
	}
	resolver := &mockGitProviderResolver{
		providers: map[int64]provider.GitProvider{10: gitProv},
	}

	spawnCounter := 0
	var reconciler3 *orchestrator.Reconciler
	mockEngine := &orchestrator.MockContainerProvider{
		SpawnRunnerFn: func(ctx context.Context, config orchestrator.RunnerConfig) (string, error) {
			spawnCounter++
			return fmt.Sprintf("quota-runner-%d", spawnCounter), nil
		},
		AuditRunnersFn: func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
			if reconciler3 == nil {
				return nil, nil
			}
			return reconciler3.TrackedPoolRunners(127), nil
		},
		PingFn: func(ctx context.Context) error {
			return nil
		},
	}
	reconciler3 = orchestrator.NewReconciler(mockEngine)

	// GlobalMaxRunners = 2. Boot creates 1 runner.
	// Reconcile needs 3 more, but can only spawn 1 more before hitting global max 2.
	// Remaining requests are enqueued.
	ctrl := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:               repo,
		ContainerEngine:  mockEngine,
		ProviderResolver: resolver,
		Reconciler:       reconciler3,
		GlobalMaxRunners: 2,
		Interval:         time.Hour,
	})

	if err := ctrl.Boot(ctx); err != nil {
		t.Fatalf("Boot failed: %v", err)
	}
	if spawnCounter != 1 {
		t.Fatalf("expected 1 runner on boot, got %d", spawnCounter)
	}

	// 4 jobs queued
	gitProv.queuedJobs = 4

	if err := ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	if ctrl.TotalActiveRunners() != 2 {
		t.Fatalf("expected 2 active runners (globalMaxRunners), got %d", ctrl.TotalActiveRunners())
	}
	if ctrl.QueueLengthForPool(127) < 1 {
		t.Fatalf("expected queued requests in internal queue due to global quota saturation, got %d",
			ctrl.QueueLengthForPool(127))
	}
}

func TestPoolController_ForgejoPollingScaling_ErrorHandledGracefully(t *testing.T) {
	ctx := context.Background()

	pool := db.RunnerPool{
		ID:             128,
		Name:           "forgejo-err",
		Provider:       "forgejo",
		RepositoryUrl:  "https://forgejo.example.com/owner/repo",
		Scope:          "repo",
		AuthProfileID:  10,
		MinIdleRunners: 2,
		MaxConcurrency: 5,
		Labels:         `["self-hosted","linux"]`,
		RunnerImage:    "ghcr.io/noosxe/runnero:latest",
	}

	repo := &mockPoolRepo{pools: []db.RunnerPool{pool}}
	gitProv := &mockGitProvider{
		scalingMode: provider.ScalingPolling,
		pollErr:     fmt.Errorf("temporary network timeout"),
	}
	resolver := &mockGitProviderResolver{
		providers: map[int64]provider.GitProvider{10: gitProv},
	}

	spawnCounter := 0
	var reconciler4 *orchestrator.Reconciler
	mockEngine := &orchestrator.MockContainerProvider{
		SpawnRunnerFn: func(ctx context.Context, config orchestrator.RunnerConfig) (string, error) {
			spawnCounter++
			return fmt.Sprintf("err-runner-%d", spawnCounter), nil
		},
		AuditRunnersFn: func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
			if reconciler4 == nil {
				return nil, nil
			}
			return reconciler4.TrackedPoolRunners(128), nil
		},
		PingFn: func(ctx context.Context) error {
			return nil
		},
	}
	reconciler4 = orchestrator.NewReconciler(mockEngine)

	ctrl := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:               repo,
		ContainerEngine:  mockEngine,
		ProviderResolver: resolver,
		Reconciler:       reconciler4,
		GlobalMaxRunners: 10,
		Interval:         time.Hour,
	})

	if err := ctrl.Boot(ctx); err != nil {
		t.Fatalf("Boot failed: %v", err)
	}

	// Reconcile must not fail even when PollQueuedJobs returns an error
	if err := ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile should not fail on polling error: %v", err)
	}

	if ctrl.TotalActiveRunners() != 2 {
		t.Fatalf("expected base 2 runners to remain active, got %d", ctrl.TotalActiveRunners())
	}
}

func TestPoolController_WebhookProviderDoesNotPoll(t *testing.T) {
	ctx := context.Background()

	pool := db.RunnerPool{
		ID:             129,
		Name:           "github-pool",
		Provider:       "github",
		RepositoryUrl:  "https://github.com/owner/repo",
		Scope:          "repo",
		AuthProfileID:  10,
		MinIdleRunners: 1,
		MaxConcurrency: 5,
		Labels:         `["self-hosted","linux"]`,
		RunnerImage:    "ghcr.io/noosxe/runnero:latest",
	}

	repo := &mockPoolRepo{pools: []db.RunnerPool{pool}}
	gitProv := &mockGitProvider{
		scalingMode: provider.ScalingWebhook,
		queuedJobs:  10,
	}
	resolver := &mockGitProviderResolver{
		providers: map[int64]provider.GitProvider{10: gitProv},
	}

	var reconciler5 *orchestrator.Reconciler
	mockEngine := &orchestrator.MockContainerProvider{
		SpawnRunnerFn: func(ctx context.Context, config orchestrator.RunnerConfig) (string, error) {
			return "github-runner-1", nil
		},
		AuditRunnersFn: func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
			if reconciler5 == nil {
				return nil, nil
			}
			return reconciler5.TrackedPoolRunners(129), nil
		},
		PingFn: func(ctx context.Context) error {
			return nil
		},
	}
	reconciler5 = orchestrator.NewReconciler(mockEngine)

	ctrl := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:               repo,
		ContainerEngine:  mockEngine,
		ProviderResolver: resolver,
		Reconciler:       reconciler5,
		GlobalMaxRunners: 10,
		Interval:         time.Hour,
	})

	if err := ctrl.Boot(ctx); err != nil {
		t.Fatalf("Boot failed: %v", err)
	}

	if err := ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// ScalingWebhook must NEVER poll
	if gitProv.pollCalls != 0 {
		t.Errorf("expected 0 poll calls for webhook-based provider, got %d", gitProv.pollCalls)
	}
}

func (m *mockJobRecorder) OpenTransitionJob(ctx context.Context, poolID int64, runnerName string, startedAt time.Time) error {
	m.opens = append(m.opens, struct {
		poolID     int64
		runnerName string
	}{poolID, runnerName})
	return nil
}

func (m *mockJobRecorder) CloseTransitionJob(ctx context.Context, poolID int64, runnerName, status string, jobID int64, logPath string, completedAt time.Time) error {
	m.closes = append(m.closes, struct {
		poolID     int64
		runnerName string
		status     string
		jobID      int64
	}{poolID, runnerName, status, jobID})
	return nil
}

func (m *mockJobRecorder) CloseInterruptedOpenJobs(ctx context.Context, completedAt time.Time) (int64, error) {
	n := m.interrupted
	m.interrupted = 0
	return n, nil
}

func (m *mockJobRecorder) CloseStaleOpenJobs(ctx context.Context, poolID int64, cutoff, completedAt time.Time) (int64, error) {
	return 0, nil
}

func (m *mockJobRecorder) RecordWebhookQueued(ctx context.Context, poolID, jobID int64, meta db.WebhookJobMeta, queuedAt time.Time) error {
	if m.failWebhook != nil {
		return m.failWebhook
	}
	m.webhookQueued = append(m.webhookQueued, webhookQueuedCall{poolID: poolID, jobID: jobID, meta: meta, queuedAt: queuedAt})
	return nil
}

func (m *mockJobRecorder) RecordWebhookStarted(ctx context.Context, poolID, jobID int64, runnerName string, startedAt, queuedAt time.Time, meta db.WebhookJobMeta) error {
	if m.failWebhook != nil {
		return m.failWebhook
	}
	m.webhookStarted = append(m.webhookStarted, webhookStartedCall{poolID: poolID, jobID: jobID, runnerName: runnerName, startedAt: startedAt, queuedAt: queuedAt, meta: meta})
	return nil
}

func (m *mockJobRecorder) RecordWebhookCompleted(ctx context.Context, poolID, jobID int64, runnerName, status string, completedAt time.Time) error {
	if m.failWebhook != nil {
		return m.failWebhook
	}
	m.webhookCompleted = append(m.webhookCompleted, webhookCompletedCall{poolID: poolID, jobID: jobID, runnerName: runnerName, status: status, completedAt: completedAt})
	return nil
}

// TestPoolControllerRecycleIdleRunners pins the idle-recycle contract of
// docs/22 §6.2: only non-busy running runners are deregistered, terminated,
// and untracked; busy runners are never touched; exited runners are left for
// audit reaping; unknown pools are a no-op; reconcile afterwards respawns the
// warm pool under the pool's current configuration.
func TestPoolControllerRecycleIdleRunners(t *testing.T) {
	ctx := context.Background()

	pool := db.RunnerPool{
		ID:             118,
		Name:           "ci-pool",
		Provider:       "github",
		RepositoryUrl:  "https://github.com/owner/repo",
		Scope:          "repo",
		AuthProfileID:  10,
		MinIdleRunners: 3,
		MaxConcurrency: 5,
		RunnerImage:    "ghcr.io/noosxe/runnero:latest",
		AllowDocker:    true,
	}

	repo := &mockPoolRepo{pools: []db.RunnerPool{pool}}
	gitProv := &mockGitProvider{}
	resolver := &mockGitProviderResolver{
		providers: map[int64]provider.GitProvider{10: gitProv},
	}

	spawnCount := 0
	mockEngine := &orchestrator.MockContainerProvider{
		SpawnRunnerFn: func(ctx context.Context, config orchestrator.RunnerConfig) (string, error) {
			spawnCount++
			return fmt.Sprintf("spawned-runner-%d", spawnCount), nil
		},
	}

	reconciler := orchestrator.NewReconciler(mockEngine)
	ctrl := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:               repo,
		ContainerEngine:  mockEngine,
		ProviderResolver: resolver,
		Reconciler:       reconciler,
	})

	// Pre-existing fleet adopted at boot: one busy, two idle, one exited.
	// Three running runners satisfy min_idle=3, so boot must not spawn.
	mockEngine.AuditRunnersFn = func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
		return []orchestrator.RunnerStatus{
			{PoolID: 118, ID: "r-busy", Name: "runnero-ci-pool-busy", PoolName: "ci-pool", State: "running", IsBusy: true},
			{PoolID: 118, ID: "r-idle-1", Name: "runnero-ci-pool-idle1", PoolName: "ci-pool", State: "running"},
			{PoolID: 118, ID: "r-idle-2", Name: "runnero-ci-pool-idle2", PoolName: "ci-pool", State: "running"},
			{PoolID: 118, ID: "r-exited", Name: "runnero-ci-pool-exited", PoolName: "ci-pool", State: "exited"},
		}, nil
	}

	if err := ctrl.Boot(ctx); err != nil {
		t.Fatalf("ctrl.Boot failed: %v", err)
	}
	if spawnCount != 0 {
		t.Fatalf("boot must not spawn (warm pool satisfied by adopted runners), got %d spawns", spawnCount)
	}

	// Recycle: idle runners deregistered, terminated, and untracked.
	if err := ctrl.RecycleIdleRunners(ctx, 118); err != nil {
		t.Fatalf("RecycleIdleRunners failed: %v", err)
	}

	terminated := terminatedIDSet(mockEngine)
	for _, id := range []string{"r-idle-1", "r-idle-2"} {
		if !terminated[id] {
			t.Errorf("idle runner %s must be terminated by recycle, terminated=%v", id, terminated)
		}
	}
	if terminated["r-busy"] {
		t.Error("busy runner must never be terminated by recycle")
	}
	if terminated["r-exited"] {
		t.Error("exited runner must not be terminated by recycle (left for audit reaping)")
	}

	if active, idle := ctrl.PoolStats(118); active != 1 || idle != 0 {
		t.Errorf("after recycle PoolStats = (active=%d, idle=%d), want (1, 0)", active, idle)
	}
	for _, name := range []string{"runnero-ci-pool-idle1", "runnero-ci-pool-idle2"} {
		if !slices.Contains(gitProv.deregistered, name) {
			t.Errorf("idle runner %s must be deregistered, deregistered=%v", name, gitProv.deregistered)
		}
	}

	// Unknown pool: no error, no side effects.
	terminationsBefore := len(mockEngine.TerminatedIDs)
	if err := ctrl.RecycleIdleRunners(ctx, 999); err != nil {
		t.Fatalf("RecycleIdleRunners(unknown pool) failed: %v", err)
	}
	if len(mockEngine.TerminatedIDs) != terminationsBefore {
		t.Error("recycling an unknown pool must not terminate anything")
	}

	// Reconcile afterwards respawns the warm pool: the busy runner survives,
	// the exited runner is dropped by audit, and two idle runners respawn.
	mockEngine.AuditRunnersFn = func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
		return []orchestrator.RunnerStatus{
			{PoolID: 118, ID: "r-busy", Name: "runnero-ci-pool-busy", PoolName: "ci-pool", State: "running", IsBusy: true},
		}, nil
	}
	if err := ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("ctrl.Reconcile failed: %v", err)
	}
	if spawnCount != 2 {
		t.Errorf("reconcile must respawn to the warm-pool target (3 active target, 1 busy surviving), got %d spawns", spawnCount)
	}
	if active, idle := ctrl.PoolStats(118); active != 1 || idle != 2 {
		t.Errorf("after reconcile PoolStats = (busy=%d, idle=%d), want (1, 2)", active, idle)
	}
	if terminatedIDSet(mockEngine)["r-busy"] {
		t.Error("busy runner must survive reconcile after recycle")
	}
}

// terminatedIDSet snapshots which container IDs the mock engine terminated.
func terminatedIDSet(engine *orchestrator.MockContainerProvider) map[string]bool {
	set := make(map[string]bool)
	for _, id := range engine.TerminatedIDs {
		set[id] = true
	}
	return set
}

// TestPoolController_RenameDoesNotDisturbRunners pins the RUN-126 invariant:
// reconciling a pool that was renamed between cycles must neither drain nor
// respawn its runners — tracking keys on the pool id, and a rename is a
// metadata-only change (docs/22 §5.4). Before RUN-126 the renamed pool looked
// like a delete followed by a create, force-terminating every runner including
// busy ones.
func TestPoolController_RenameDoesNotDisturbRunners(t *testing.T) {
	ctx := context.Background()

	pool := db.RunnerPool{
		ID:             121,
		Name:           "ci-race",
		Provider:       "github",
		RepositoryUrl:  "https://github.com/owner/repo",
		Scope:          "repo",
		AuthProfileID:  10,
		MinIdleRunners: 1,
		MaxConcurrency: 5,
		Labels:         `["self-hosted"]`,
		RunnerImage:    "ghcr.io/noosxe/runnero:latest",
		AllowDocker:    true,
	}
	repo := &mockPoolRepo{pools: []db.RunnerPool{pool}}
	gitProv := &mockGitProvider{}
	resolver := &mockGitProviderResolver{providers: map[int64]provider.GitProvider{10: gitProv}}

	spawnCount := 0
	mockEngine := &orchestrator.MockContainerProvider{
		SpawnRunnerFn: func(ctx context.Context, config orchestrator.RunnerConfig) (string, error) {
			spawnCount++
			if config.PoolID != 121 {
				t.Errorf("spawn must carry the pool id, got %d", config.PoolID)
			}
			return fmt.Sprintf("spawned-runner-%d", spawnCount), nil
		},
	}

	reconciler := orchestrator.NewReconciler(mockEngine)
	ctrl := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:               repo,
		ContainerEngine:  mockEngine,
		ProviderResolver: resolver,
		Reconciler:       reconciler,
	})

	// A busy runner mid-job, tracked and live.
	mockEngine.AuditRunnersFn = func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
		return []orchestrator.RunnerStatus{
			{PoolID: 121, ID: "r-busy", Name: "runnero-ci-race-busy", PoolName: "ci-race", State: "running", IsBusy: true},
		}, nil
	}

	if err := ctrl.Boot(ctx); err != nil {
		t.Fatalf("Boot failed: %v", err)
	}
	if spawnCount != 0 {
		t.Fatalf("warm pool satisfied by the busy runner; want 0 spawns, got %d", spawnCount)
	}

	// The pool is renamed out from under the controller; same id, same config.
	renamed := pool
	renamed.Name = "ci-race-renamed"
	repo.pools = []db.RunnerPool{renamed}

	if err := ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile after rename failed: %v", err)
	}

	if terminated := terminatedIDSet(mockEngine); terminated["r-busy"] {
		t.Fatal("renaming a pool must never terminate its runners (busy runner was drained)")
	}
	if spawnCount != 0 {
		t.Errorf("rename must not respawn runners, got %d spawns", spawnCount)
	}
	if active, idle := ctrl.PoolStats(121); active != 1 || idle != 0 {
		t.Errorf("tracking must survive the rename, PoolStats=(%d, %d), want (1, 0)", active, idle)
	}
	if runners := reconciler.TrackedPoolRunners(121); len(runners) != 1 || runners[0].ID != "r-busy" {
		t.Errorf("runner must remain tracked under the pool id: %+v", runners)
	}

	// The next spawn after the rename carries the new name (readability only).
	var lastSpawnConfig *orchestrator.RunnerConfig
	mockEngine.SpawnRunnerFn = func(ctx context.Context, config orchestrator.RunnerConfig) (string, error) {
		cfg := config
		lastSpawnConfig = &cfg
		return "respawned-runner", nil
	}
	mockEngine.AuditRunnersFn = func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
		return nil, nil
	}
	if err := ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile after rename+drain failed: %v", err)
	}
	if lastSpawnConfig == nil {
		t.Fatal("expected the pool to respawn its warm runner once untracked")
	}
	if lastSpawnConfig.PoolName != "ci-race-renamed" || lastSpawnConfig.PoolID != 121 {
		t.Errorf("respawn must carry new name and stable id, got name=%q id=%d", lastSpawnConfig.PoolName, lastSpawnConfig.PoolID)
	}
}
