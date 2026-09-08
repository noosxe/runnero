package orchestrator_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/noosxe/runnero/internal/db"
	"github.com/noosxe/runnero/internal/orchestrator"
	"github.com/noosxe/runnero/internal/provider"
	"github.com/noosxe/runnero/internal/webhook"
)

// TestPoolController_ScaleToZero_HoldsZeroContainersWhenIdle verifies that a webhook pool
// with min_idle_runners=0 boots with zero containers, and subsequent reconciliation cycles
// do not replenish idle runners (docs/01 §3 Phase 3, RUN-71).
func TestPoolController_ScaleToZero_HoldsZeroContainersWhenIdle(t *testing.T) {
	ctx := context.Background()

	pool := db.RunnerPool{
		ID:             159,
		Name:           "scale-zero-pool",
		Provider:       "github",
		RepositoryUrl:  "https://github.com/test-org/scale-zero-repo",
		Scope:          "repo",
		AuthProfileID:  10,
		MinIdleRunners: 0, // Scale-to-zero enabled
		MaxConcurrency: 5,
		Labels:         `["self-hosted","linux"]`,
		RunnerImage:    "ghcr.io/noosxe/runnero:latest",
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
			return fmt.Sprintf("container-%d", spawnCount), nil
		},
		AuditRunnersFn: func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
			return nil, nil
		},
		PingFn: func(ctx context.Context) error {
			return nil
		},
	}

	controller := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:               repo,
		ContainerEngine:  mockEngine,
		ProviderResolver: resolver,
		GlobalMaxRunners: 10,
		Interval:         time.Hour,
	})

	// 1. Boot sequence must not provision any runners when min_idle_runners=0
	if err := controller.Boot(ctx); err != nil {
		t.Fatalf("boot failed: %v", err)
	}

	if spawnCount != 0 {
		t.Fatalf("expected 0 runners spawned on boot for scale-to-zero pool, got %d", spawnCount)
	}
	if controller.TotalActiveRunners() != 0 {
		t.Fatalf("expected 0 active runners, got %d", controller.TotalActiveRunners())
	}

	// 2. Subsequent Reconcile cycles must not replenish zero-target pools
	for i := 0; i < 3; i++ {
		if err := controller.Reconcile(ctx); err != nil {
			t.Fatalf("reconcile cycle %d failed: %v", i, err)
		}
	}

	if spawnCount != 0 {
		t.Fatalf("expected 0 runners spawned after multiple reconcile cycles, got %d", spawnCount)
	}
	if controller.TotalActiveRunners() != 0 {
		t.Fatalf("expected 0 active runners after multiple reconcile cycles, got %d", controller.TotalActiveRunners())
	}
}

// TestPoolController_ScaleToZero_QueuedEventSpawnsOnDemandAndReconcilePreserves verifies that:
//  1. Webhook queued event triggers immediate on-demand runner provisioning from 0 containers.
//  2. Audit loop / Reconcile cycle preserves the on-demand idle runner during its startup grace period
//     and does not terminate it or spawn duplicate replenishments (RUN-71).
func TestPoolController_ScaleToZero_QueuedEventSpawnsOnDemandAndReconcilePreserves(t *testing.T) {
	ctx := context.Background()

	pool := db.RunnerPool{
		ID:             155,
		Name:           "zero-webhook-pool",
		Provider:       "github",
		RepositoryUrl:  "https://github.com/test-org/zero-repo",
		Scope:          "repo",
		AuthProfileID:  10,
		MinIdleRunners: 0,
		MaxConcurrency: 5,
		Labels:         `["self-hosted","linux"]`,
		RunnerImage:    "ghcr.io/noosxe/runnero:latest",
	}

	repo := &mockPoolRepo{pools: []db.RunnerPool{pool}}
	gitProv := &mockGitProvider{}
	resolver := &mockGitProviderResolver{
		providers: map[int64]provider.GitProvider{10: gitProv},
	}

	var mu sync.Mutex
	spawnCount := 0
	terminated := make([]string, 0)
	var reconciler *orchestrator.Reconciler

	mockEngine := &orchestrator.MockContainerProvider{
		SpawnRunnerFn: func(ctx context.Context, config orchestrator.RunnerConfig) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			spawnCount++
			return fmt.Sprintf("runner-container-%d", spawnCount), nil
		},
		AuditRunnersFn: func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
			if reconciler == nil {
				return nil, nil
			}
			return reconciler.TrackedPoolRunners(155), nil
		},
		TerminateRunnerFn: func(ctx context.Context, containerID string) error {
			mu.Lock()
			defer mu.Unlock()
			terminated = append(terminated, containerID)
			return nil
		},
		PingFn: func(ctx context.Context) error {
			return nil
		},
	}
	reconciler = orchestrator.NewReconciler(mockEngine)

	controller := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:                     repo,
		ContainerEngine:        mockEngine,
		ProviderResolver:       resolver,
		Reconciler:             reconciler,
		GlobalMaxRunners:       10,
		Interval:               time.Hour,
		ScaleToZeroGracePeriod: 5 * time.Minute,
	})

	if err := controller.Boot(ctx); err != nil {
		t.Fatalf("boot failed: %v", err)
	}
	if controller.TotalActiveRunners() != 0 {
		t.Fatalf("expected 0 active runners initially, got %d", controller.TotalActiveRunners())
	}

	// Webhook queued event arrives -> immediate on-demand spawn
	evt := &webhook.WorkflowJobEvent{
		Action: "queued",
		Repository: webhook.RepositoryPayload{
			FullName: "test-org/zero-repo",
			HTMLURL:  "https://github.com/test-org/zero-repo",
		},
		WorkflowJob: webhook.WorkflowJobPayload{
			ID:     7001,
			Labels: []string{"self-hosted", "linux"},
		},
	}

	if err := controller.HandleWorkflowJob(ctx, "github", evt); err != nil {
		t.Fatalf("HandleWorkflowJob failed: %v", err)
	}

	if spawnCount != 1 {
		t.Fatalf("expected 1 runner spawned on demand, got %d", spawnCount)
	}
	if controller.TotalActiveRunners() != 1 {
		t.Fatalf("expected 1 active runner, got %d", controller.TotalActiveRunners())
	}

	// Reconcile cycle runs while runner is starting up (idle, !IsBusy)
	if err := controller.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// The runner must NOT be terminated by the reconciler during its grace period
	if len(terminated) != 0 {
		t.Fatalf("runner was prematurely terminated by reconciler: %v", terminated)
	}
	if controller.TotalActiveRunners() != 1 {
		t.Fatalf("expected runner to remain active, got %d", controller.TotalActiveRunners())
	}
	if spawnCount != 1 {
		t.Fatalf("reconciler must not replenish zero-target pool, spawned: %d", spawnCount)
	}
}

// TestPoolController_ScaleToZero_LifecycleFullLoopEphemerallyReturnsToZero verifies:
// 1. Queued event scales up from 0 to 1
// 2. in_progress event marks runner busy
// 3. completed event unmarks runner busy
// 4. Container terminates/exits ephemerally
// 5. Reconcile reaps exited container and cleanly returns pool to zero containers (RUN-71).
func TestPoolController_ScaleToZero_LifecycleFullLoopEphemerallyReturnsToZero(t *testing.T) {
	ctx := context.Background()

	pool := db.RunnerPool{
		ID:             156,
		Name:           "lifecycle-zero-pool",
		Provider:       "github",
		RepositoryUrl:  "https://github.com/test-org/lifecycle-repo",
		Scope:          "repo",
		AuthProfileID:  10,
		MinIdleRunners: 0,
		MaxConcurrency: 5,
		Labels:         `["self-hosted","linux"]`,
		RunnerImage:    "ghcr.io/noosxe/runnero:latest",
	}

	repo := &mockPoolRepo{pools: []db.RunnerPool{pool}}
	gitProv := &mockGitProvider{}
	resolver := &mockGitProviderResolver{
		providers: map[int64]provider.GitProvider{10: gitProv},
	}

	var mu sync.Mutex
	spawnCount := 0
	containerState := "running"
	var reconciler *orchestrator.Reconciler

	mockEngine := &orchestrator.MockContainerProvider{
		SpawnRunnerFn: func(ctx context.Context, config orchestrator.RunnerConfig) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			spawnCount++
			return fmt.Sprintf("runner-%d", spawnCount), nil
		},
		AuditRunnersFn: func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
			mu.Lock()
			defer mu.Unlock()
			if reconciler == nil || spawnCount == 0 {
				return nil, nil
			}
			runners := reconciler.TrackedPoolRunners(156)
			for i := range runners {
				runners[i].State = containerState
			}
			return runners, nil
		},
		TerminateRunnerFn: func(ctx context.Context, containerID string) error {
			return nil
		},
		PingFn: func(ctx context.Context) error {
			return nil
		},
	}
	reconciler = orchestrator.NewReconciler(mockEngine)

	controller := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:               repo,
		ContainerEngine:  mockEngine,
		ProviderResolver: resolver,
		Reconciler:       reconciler,
		GlobalMaxRunners: 10,
		Interval:         time.Hour,
	})

	if err := controller.Boot(ctx); err != nil {
		t.Fatalf("boot failed: %v", err)
	}

	// 1. Queued webhook event
	queuedEvt := &webhook.WorkflowJobEvent{
		Action: "queued",
		Repository: webhook.RepositoryPayload{
			FullName: "test-org/lifecycle-repo",
			HTMLURL:  "https://github.com/test-org/lifecycle-repo",
		},
		WorkflowJob: webhook.WorkflowJobPayload{
			ID:     8001,
			Labels: []string{"self-hosted", "linux"},
		},
	}
	if err := controller.HandleWorkflowJob(ctx, "github", queuedEvt); err != nil {
		t.Fatalf("queued event failed: %v", err)
	}

	runners := reconciler.TrackedPoolRunners(156)
	if len(runners) != 1 {
		t.Fatalf("expected 1 tracked runner, got %d", len(runners))
	}
	runnerName := runners[0].Name

	// 2. in_progress event
	inProgressEvt := &webhook.WorkflowJobEvent{
		Action: "in_progress",
		Repository: webhook.RepositoryPayload{
			FullName: "test-org/lifecycle-repo",
			HTMLURL:  "https://github.com/test-org/lifecycle-repo",
		},
		WorkflowJob: webhook.WorkflowJobPayload{
			ID:         8001,
			RunnerName: runnerName,
		},
	}
	if err := controller.HandleWorkflowJob(ctx, "github", inProgressEvt); err != nil {
		t.Fatalf("in_progress event failed: %v", err)
	}

	runners = reconciler.TrackedPoolRunners(156)
	if !runners[0].IsBusy {
		t.Fatalf("expected runner to be marked busy")
	}

	// 3. completed event
	completedEvt := &webhook.WorkflowJobEvent{
		Action: "completed",
		Repository: webhook.RepositoryPayload{
			FullName: "test-org/lifecycle-repo",
			HTMLURL:  "https://github.com/test-org/lifecycle-repo",
		},
		WorkflowJob: webhook.WorkflowJobPayload{
			ID:         8001,
			RunnerName: runnerName,
		},
	}
	if err := controller.HandleWorkflowJob(ctx, "github", completedEvt); err != nil {
		t.Fatalf("completed event failed: %v", err)
	}

	// 4. Ephemeral runner exits
	mu.Lock()
	containerState = "exited"
	mu.Unlock()

	// 5. Next Reconcile cycle audits, reaps exited container, and leaves pool at 0
	if err := controller.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	if controller.TotalActiveRunners() != 0 {
		t.Fatalf("expected 0 active runners after reaping completed runner, got %d", controller.TotalActiveRunners())
	}
	if len(reconciler.TrackedPoolRunners(156)) != 0 {
		t.Fatalf("expected 0 tracked runners, got %d", len(reconciler.TrackedPoolRunners(156)))
	}

	// Verify no subsequent replenishment
	if err := controller.Reconcile(ctx); err != nil {
		t.Fatalf("subsequent Reconcile failed: %v", err)
	}
	if controller.TotalActiveRunners() != 0 {
		t.Fatalf("expected pool to stay at zero, got %d", controller.TotalActiveRunners())
	}
}

// TestPoolController_ScaleToZero_StaleOrphanedRunnerDrainedAfterGracePeriod verifies that
// if an on-demand runner is spawned but the job was cancelled or never assigned, the reconciler
// drains and terminates it once the grace period expires (RUN-71).
func TestPoolController_ScaleToZero_StaleOrphanedRunnerDrainedAfterGracePeriod(t *testing.T) {
	ctx := context.Background()

	pool := db.RunnerPool{
		ID:             157,
		Name:           "stale-zero-pool",
		Provider:       "github",
		RepositoryUrl:  "https://github.com/test-org/stale-repo",
		Scope:          "repo",
		AuthProfileID:  10,
		MinIdleRunners: 0,
		MaxConcurrency: 5,
		Labels:         `["self-hosted","linux"]`,
		RunnerImage:    "ghcr.io/noosxe/runnero:latest",
	}

	repo := &mockPoolRepo{pools: []db.RunnerPool{pool}}
	gitProv := &mockGitProvider{}
	resolver := &mockGitProviderResolver{
		providers: map[int64]provider.GitProvider{10: gitProv},
	}

	var mu sync.Mutex
	terminated := make([]string, 0)
	var reconciler *orchestrator.Reconciler

	mockEngine := &orchestrator.MockContainerProvider{
		SpawnRunnerFn: func(ctx context.Context, config orchestrator.RunnerConfig) (string, error) {
			return "stale-runner-1", nil
		},
		AuditRunnersFn: func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
			if reconciler == nil {
				return nil, nil
			}
			return reconciler.TrackedPoolRunners(157), nil
		},
		TerminateRunnerFn: func(ctx context.Context, containerID string) error {
			mu.Lock()
			defer mu.Unlock()
			terminated = append(terminated, containerID)
			return nil
		},
		PingFn: func(ctx context.Context) error {
			return nil
		},
	}
	reconciler = orchestrator.NewReconciler(mockEngine)

	// Set a very short grace period: 50ms
	controller := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:                     repo,
		ContainerEngine:        mockEngine,
		ProviderResolver:       resolver,
		Reconciler:             reconciler,
		GlobalMaxRunners:       10,
		Interval:               time.Hour,
		ScaleToZeroGracePeriod: 50 * time.Millisecond,
	})

	if err := controller.Boot(ctx); err != nil {
		t.Fatalf("boot failed: %v", err)
	}

	// Spawn an on-demand runner via webhook
	evt := &webhook.WorkflowJobEvent{
		Action: "queued",
		Repository: webhook.RepositoryPayload{
			FullName: "test-org/stale-repo",
			HTMLURL:  "https://github.com/test-org/stale-repo",
		},
		WorkflowJob: webhook.WorkflowJobPayload{
			ID:     9001,
			Labels: []string{"self-hosted", "linux"},
		},
	}
	if err := controller.HandleWorkflowJob(ctx, "github", evt); err != nil {
		t.Fatalf("queued event failed: %v", err)
	}
	if controller.TotalActiveRunners() != 1 {
		t.Fatalf("expected 1 active runner, got %d", controller.TotalActiveRunners())
	}

	// 1. Immediately reconcile: within grace period (0ms < 50ms) -> NOT terminated
	if err := controller.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	if len(terminated) != 0 {
		t.Fatalf("expected 0 terminated runners within grace period, got %d", len(terminated))
	}

	// 2. Wait for grace period to elapse
	time.Sleep(70 * time.Millisecond)

	// 3. Reconcile after grace period: runner was never assigned a job -> drained
	if err := controller.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(terminated) != 1 {
		t.Fatalf("expected orphaned runner to be drained after grace period, got %d terminated", len(terminated))
	}
	if controller.TotalActiveRunners() != 0 {
		t.Fatalf("expected 0 active runners after draining stale runner, got %d", controller.TotalActiveRunners())
	}
}

// TestPoolController_ScaleToZero_LiveTransitionFromStandbyDrainsToZero verifies that when
// a pool is switched from min_idle_runners > 0 to min_idle_runners = 0 live, existing standby
// runners are immediately drained down to zero (RUN-71).
func TestPoolController_ScaleToZero_LiveTransitionFromStandbyDrainsToZero(t *testing.T) {
	ctx := context.Background()

	pool := db.RunnerPool{
		ID:             158,
		Name:           "standby-to-zero-pool",
		Provider:       "github",
		RepositoryUrl:  "https://github.com/test-org/standby-repo",
		Scope:          "repo",
		AuthProfileID:  10,
		MinIdleRunners: 2, // Starts with 2 warm standby runners
		MaxConcurrency: 5,
		Labels:         `["self-hosted","linux"]`,
		RunnerImage:    "ghcr.io/noosxe/runnero:latest",
	}

	repo := &mockPoolRepo{pools: []db.RunnerPool{pool}}
	gitProv := &mockGitProvider{}
	resolver := &mockGitProviderResolver{
		providers: map[int64]provider.GitProvider{10: gitProv},
	}

	var mu sync.Mutex
	spawnCount := 0
	terminated := make([]string, 0)
	var reconciler *orchestrator.Reconciler

	mockEngine := &orchestrator.MockContainerProvider{
		SpawnRunnerFn: func(ctx context.Context, config orchestrator.RunnerConfig) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			spawnCount++
			return fmt.Sprintf("standby-%d", spawnCount), nil
		},
		AuditRunnersFn: func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
			if reconciler == nil {
				return nil, nil
			}
			return reconciler.TrackedPoolRunners(158), nil
		},
		TerminateRunnerFn: func(ctx context.Context, containerID string) error {
			mu.Lock()
			defer mu.Unlock()
			terminated = append(terminated, containerID)
			return nil
		},
		PingFn: func(ctx context.Context) error {
			return nil
		},
	}
	reconciler = orchestrator.NewReconciler(mockEngine)

	controller := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:               repo,
		ContainerEngine:  mockEngine,
		ProviderResolver: resolver,
		Reconciler:       reconciler,
		GlobalMaxRunners: 10,
		Interval:         time.Hour,
	})

	// Boot creates 2 standby runners
	if err := controller.Boot(ctx); err != nil {
		t.Fatalf("boot failed: %v", err)
	}
	if controller.TotalActiveRunners() != 2 {
		t.Fatalf("expected 2 active runners initially, got %d", controller.TotalActiveRunners())
	}

	// Live pool update: set min_idle_runners = 0 (scale-to-zero)
	repo.pools[0].MinIdleRunners = 0

	if err := controller.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// Standby runners should be drained immediately
	mu.Lock()
	defer mu.Unlock()
	if len(terminated) != 2 {
		t.Fatalf("expected 2 standby runners to be drained, got %d", len(terminated))
	}
	if controller.TotalActiveRunners() != 0 {
		t.Fatalf("expected 0 active runners, got %d", controller.TotalActiveRunners())
	}
}

// TestPoolController_ScaleToZero_LifetimeNeverCapsGracePeriod verifies docs/23
// §4.6: the lifetime switch is busy-only, so it no longer shortens the
// on-demand startup grace. An orphaned on-demand idle runner older than the
// pool's max_runner_lifetime_seconds but younger than the grace period must
// survive; only exceeding the full grace period drains it.
func TestPoolController_ScaleToZero_LifetimeNeverCapsGracePeriod(t *testing.T) {
	ctx := context.Background()

	pool := db.RunnerPool{
		ID:                       158,
		Name:                     "uncapped-grace-pool",
		Provider:                 "github",
		RepositoryUrl:            "https://github.com/test-org/uncapped-repo",
		Scope:                    "repo",
		AuthProfileID:            10,
		MinIdleRunners:           0,
		MaxConcurrency:           5,
		Labels:                   `["self-hosted","linux"]`,
		RunnerImage:              "ghcr.io/noosxe/runnero:latest",
		MaxRunnerLifetimeSeconds: 60, // 1 minute — far below the 5 minute grace
	}

	repo := &mockPoolRepo{pools: []db.RunnerPool{pool}}
	gitProv := &mockGitProvider{}
	resolver := &mockGitProviderResolver{
		providers: map[int64]provider.GitProvider{10: gitProv},
	}

	var mu sync.Mutex
	terminated := make([]string, 0)
	var reconciler *orchestrator.Reconciler

	mockEngine := &orchestrator.MockContainerProvider{
		AuditRunnersFn: func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
			if reconciler == nil {
				return nil, nil
			}
			return reconciler.TrackedPoolRunners(158), nil
		},
		TerminateRunnerFn: func(ctx context.Context, containerID string) error {
			mu.Lock()
			defer mu.Unlock()
			terminated = append(terminated, containerID)
			return nil
		},
		PingFn: func(ctx context.Context) error {
			return nil
		},
	}
	reconciler = orchestrator.NewReconciler(mockEngine)

	controller := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:                     repo,
		ContainerEngine:        mockEngine,
		ProviderResolver:       resolver,
		Reconciler:             reconciler,
		GlobalMaxRunners:       10,
		Interval:               time.Hour,
		ScaleToZeroGracePeriod: 5 * time.Minute,
	})

	if err := controller.Boot(ctx); err != nil {
		t.Fatalf("boot failed: %v", err)
	}

	track := func(spawnedAgo time.Duration) {
		t.Helper()
		status := orchestrator.RunnerStatus{
			PoolID:    158,
			ID:        "orphan-1",
			Name:      "runnero-orphan-1",
			PoolName:  pool.Name,
			State:     "running",
			OnDemand:  true,
			IsBusy:    false,
			SpawnedAt: time.Now().UTC().Add(-spawnedAgo),
		}
		reconciler.UntrackRunner(158, status.ID)
		reconciler.TrackRunner(status)
	}

	// 90s old: past the 60s lifetime, inside the 5min grace. The old
	// min(grace, lifetime) cap drained here; docs/23 §4.6 keeps it alive.
	track(90 * time.Second)
	if err := controller.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	mu.Lock()
	if len(terminated) != 0 {
		mu.Unlock()
		t.Fatalf("orphaned on-demand runner within grace must not be drained even when past the lifetime, got %v", terminated)
	}
	mu.Unlock()
	if got := len(reconciler.TrackedPoolRunners(158)); got != 1 {
		t.Fatalf("runner must remain tracked within grace period, got %d", got)
	}

	// 6min old: past the full grace period — drained, lifetime irrelevant.
	track(6 * time.Minute)
	if err := controller.Reconcile(ctx); err != nil {
		t.Fatalf("second reconcile failed: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(terminated) != 1 {
		t.Fatalf("orphaned on-demand runner past the grace period must be drained, got %v", terminated)
	}
	if controller.TotalActiveRunners() != 0 {
		t.Fatalf("expected 0 active runners after draining, got %d", controller.TotalActiveRunners())
	}
}
