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

// Regression tests for the runner spawn pipeline:
//
//   - RUN-156: fixed-idle pools must not drain on-demand runners during their
//     startup grace period (the webhook-provisioned runner would be killed
//     between spawn and first job pickup).
//   - RUN-151: demand-polling spawns must be directed at the target with
//     queued jobs, and idle runners on other targets must not mask demand.

// perTargetPollProvider returns per-target queued-job counts for demand
// polling, falling back to 0 for unknown targets.
type perTargetPollProvider struct {
	*mockGitProvider
	mu     sync.Mutex
	queued map[string]int
}

func (p *perTargetPollProvider) PollQueuedJobs(ctx context.Context, target provider.PollTarget) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pollCalls++
	return p.queued[target.URL], nil
}

// spawnPipelineHarness wires a single multi-target pool with a per-target
// demand mock and records every spawned config and terminated container.
type spawnPipelineHarness struct {
	ctrl       *orchestrator.PoolController
	reconciler *orchestrator.Reconciler
	prov       *perTargetPollProvider

	mu             sync.Mutex
	spawnedConfigs []orchestrator.RunnerConfig
	terminated     []string
}

func setupSpawnPipelineHarness(t *testing.T, pool db.RunnerPool, targets []string, queued map[string]int, grace time.Duration) *spawnPipelineHarness {
	t.Helper()

	prov := &perTargetPollProvider{
		mockGitProvider: &mockGitProvider{scalingMode: provider.ScalingWebhook},
		queued:          queued,
	}
	resolver := &mockGitProviderResolver{
		providers: map[int64]provider.GitProvider{10: prov},
	}

	targetRows := make([]db.PoolTarget, 0, len(targets))
	for _, u := range targets {
		targetRows = append(targetRows, db.PoolTarget{PoolID: pool.ID, TargetUrl: u})
	}
	mockDB := &mockMultiTargetDB{
		pools:   []db.RunnerPool{pool},
		targets: map[int64][]db.PoolTarget{pool.ID: targetRows},
	}

	h := &spawnPipelineHarness{prov: prov}

	var reconciler *orchestrator.Reconciler
	mockEngine := &orchestrator.MockContainerProvider{
		SpawnRunnerFn: func(ctx context.Context, config orchestrator.RunnerConfig) (string, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.spawnedConfigs = append(h.spawnedConfigs, config)
			return fmt.Sprintf("cnt-%d", len(h.spawnedConfigs)), nil
		},
		AuditRunnersFn: func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
			if reconciler == nil {
				return nil, nil
			}
			return reconciler.TrackedPoolRunners(pool.ID), nil
		},
		TerminateRunnerFn: func(ctx context.Context, containerID string) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.terminated = append(h.terminated, containerID)
			return nil
		},
		PingFn: func(ctx context.Context) error { return nil },
	}
	reconciler = orchestrator.NewReconciler(mockEngine)
	h.reconciler = reconciler

	h.ctrl = orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:                     mockDB,
		ContainerEngine:        mockEngine,
		ProviderResolver:       resolver,
		Reconciler:             reconciler,
		GlobalMaxRunners:       10,
		Interval:               time.Hour,
		ScaleToZeroGracePeriod: grace,
	})
	return h
}

func (h *spawnPipelineHarness) spawnTargets() map[string]int {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]int, len(h.spawnedConfigs))
	for _, cfg := range h.spawnedConfigs {
		out[cfg.RepoURL]++
	}
	return out
}

func (h *spawnPipelineHarness) spawnCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.spawnedConfigs)
}

func (h *spawnPipelineHarness) terminatedIDs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.terminated...)
}

// spawnPipelinePool is a fixed-idle (min_idle=1) repo-scope pool with
// poll_fallback enabled, so demand spawns are on-demand runners.
func spawnPipelinePool(id int64) db.RunnerPool {
	return db.RunnerPool{
		ID:             id,
		Name:           fmt.Sprintf("spawn-pipeline-%d", id),
		Provider:       "github",
		RepositoryUrl:  "https://github.com/acme/repo-a",
		Scope:          "repo",
		AuthProfileID:  10,
		MinIdleRunners: 1,
		MaxConcurrency: 5,
		Labels:         `["self-hosted","linux"]`,
		RunnerImage:    "ghcr.io/noosxe/runnero:latest",
		PollFallback:   true,
	}
}

// RUN-156: a webhook-provisioned on-demand runner must survive reconcile ticks
// inside its startup grace period even when it counts as excess idle
// (min_idle=1, 2 idle runners). Before the fix, the tick following a queued
// webhook event drained the fresh runner before it could register and pick up
// the job.
func TestSpawnPipeline_FixedIdle_WebhookSpawnSurvivesReconcileInsideGrace(t *testing.T) {
	ctx := context.Background()
	targetA := "https://github.com/acme/repo-a"
	targetB := "https://github.com/acme/repo-b"
	h := setupSpawnPipelineHarness(t, spawnPipelinePool(401), []string{targetA, targetB}, nil, 5*time.Minute)

	if err := h.ctrl.Boot(ctx); err != nil {
		t.Fatalf("Boot failed: %v", err)
	}
	if h.spawnCount() != 1 {
		t.Fatalf("expected 1 min-idle runner on boot, got %d", h.spawnCount())
	}

	// Webhook queued events provision an on-demand runner once demand exceeds
	// the warm runner: the first event is covered warm-first (no spawn), the
	// second books demand beyond idle capacity and provisions the shortfall.
	for _, jobID := range []int64{9001, 9003} {
		evt := &webhook.WorkflowJobEvent{
			Action: "queued",
			Repository: webhook.RepositoryPayload{
				FullName: "acme/repo-a",
				HTMLURL:  targetA,
			},
			WorkflowJob: webhook.WorkflowJobPayload{
				ID:     jobID,
				Labels: []string{"self-hosted", "linux"},
			},
		}
		if err := h.ctrl.HandleWorkflowJob(ctx, "github", evt); err != nil {
			t.Fatalf("HandleWorkflowJob (%d) failed: %v", jobID, err)
		}
	}

	if h.spawnCount() != 2 {
		t.Fatalf("expected webhook to provision only the shortfall (2 total), got %d spawns", h.spawnCount())
	}

	// Reconcile tick lands inside the fresh runner's startup window.
	if err := h.ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	if got := h.terminatedIDs(); len(got) != 0 {
		t.Fatalf("on-demand runner drained during startup grace period: %v", got)
	}
	if h.ctrl.TotalActiveRunners() != 2 {
		t.Fatalf("expected both runners to remain active, got %d", h.ctrl.TotalActiveRunners())
	}
}

// RUN-156: once an on-demand runner is past the grace period, it drains as
// excess — but a fresh on-demand runner sharing the pool must be spared.
func TestSpawnPipeline_FixedIdle_StaleOnDemandDrainedFreshSpared(t *testing.T) {
	ctx := context.Background()
	targetA := "https://github.com/acme/repo-a"
	targetB := "https://github.com/acme/repo-b"
	h := setupSpawnPipelineHarness(t, spawnPipelinePool(402), []string{targetA, targetB}, nil, 5*time.Minute)

	if err := h.ctrl.Boot(ctx); err != nil {
		t.Fatalf("Boot failed: %v", err)
	}
	// Demand beyond the warm runner provisions the on-demand runner: the
	// first event is covered warm-first, the second spawns the shortfall.
	for _, jobID := range []int64{9002, 9004} {
		evt := &webhook.WorkflowJobEvent{
			Action: "queued",
			Repository: webhook.RepositoryPayload{
				FullName: "acme/repo-a",
				HTMLURL:  targetA,
			},
			WorkflowJob: webhook.WorkflowJobPayload{
				ID:     jobID,
				Labels: []string{"self-hosted", "linux"},
			},
		}
		if err := h.ctrl.HandleWorkflowJob(ctx, "github", evt); err != nil {
			t.Fatalf("HandleWorkflowJob (%d) failed: %v", jobID, err)
		}
	}

	// Age the boot runner past the grace period (it never picked up a job).
	tracked := h.reconciler.TrackedPoolRunners(402)
	if len(tracked) != 2 {
		t.Fatalf("expected 2 tracked runners, got %d", len(tracked))
	}
	var stale orchestrator.RunnerStatus
	for _, r := range tracked {
		if r.Name == "runner-1" || r.ID == "cnt-1" {
			stale = r
		}
	}
	if stale.ID == "" {
		t.Fatalf("could not find the boot runner among %v", tracked)
	}
	stale.SpawnedAt = time.Now().UTC().Add(-10 * time.Minute)
	h.reconciler.TrackRunner(stale)

	if err := h.ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	got := h.terminatedIDs()
	if len(got) != 1 || got[0] != stale.ID {
		t.Fatalf("expected exactly the stale runner %q to drain, got %v", stale.ID, got)
	}
	if h.ctrl.TotalActiveRunners() != 1 {
		t.Fatalf("expected fresh runner to remain active, got %d", h.ctrl.TotalActiveRunners())
	}
}

// RUN-42 regression guard: standby (non-on-demand) excess idle runners still
// drain immediately in fixed-idle pools — the RUN-156 grace only covers
// on-demand runners.
func TestSpawnPipeline_FixedIdle_StandbyExcessStillDrainsImmediately(t *testing.T) {
	ctx := context.Background()
	targetA := "https://github.com/acme/repo-a"
	pool := spawnPipelinePool(403)
	pool.PollFallback = false // pure webhook pool: replenish spawns are standbys
	h := setupSpawnPipelineHarness(t, pool, []string{targetA}, nil, 5*time.Minute)

	if err := h.ctrl.Boot(ctx); err != nil {
		t.Fatalf("Boot failed: %v", err)
	}

	// Manually track two extra idle standbys → 3 idle vs target 1.
	for i := 2; i <= 3; i++ {
		h.reconciler.TrackRunner(orchestrator.RunnerStatus{
			ID:        fmt.Sprintf("cnt-%d", i),
			Name:      fmt.Sprintf("standby-%d", i),
			PoolName:  pool.Name,
			PoolID:    pool.ID,
			State:     "running",
			SpawnedAt: time.Now().UTC(),
			TargetURL: targetA,
		})
	}

	if err := h.ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	if got := h.terminatedIDs(); len(got) != 2 {
		t.Fatalf("expected 2 excess standbys to drain, got %v", got)
	}
	if h.ctrl.TotalActiveRunners() != 1 {
		t.Fatalf("expected 1 runner to remain, got %d", h.ctrl.TotalActiveRunners())
	}
}

// RUN-151: demand spawns must go to the target with queued jobs, not
// round-robin across all pool targets.
func TestSpawnPipeline_PollFallback_SpawnsDirectedAtDemandTarget(t *testing.T) {
	ctx := context.Background()
	targetA := "https://github.com/acme/repo-a"
	targetB := "https://github.com/acme/repo-b"
	queued := map[string]int{targetA: 2, targetB: 0}
	h := setupSpawnPipelineHarness(t, spawnPipelinePool(404), []string{targetA, targetB}, queued, time.Minute)

	if err := h.ctrl.Boot(ctx); err != nil {
		t.Fatalf("Boot failed: %v", err)
	}

	if h.spawnCount() != 2 {
		t.Fatalf("expected 2 deficit spawns, got %d", h.spawnCount())
	}
	got := h.spawnTargets()
	if got[targetA] != 2 {
		t.Fatalf("expected both spawns on the demanding target %s, got %v", targetA, got)
	}
	if got[targetB] != 0 {
		t.Fatalf("expected no spawns on the idle target %s, got %v", targetB, got)
	}
}

// RUN-151: idle runners registered on one target must not mask demand on
// another — the deficit must be computed per target.
func TestSpawnPipeline_PollFallback_IdleOnOtherTargetDoesNotMaskDemand(t *testing.T) {
	ctx := context.Background()
	targetA := "https://github.com/acme/repo-a"
	targetB := "https://github.com/acme/repo-b"
	queued := map[string]int{targetA: 1, targetB: 0}
	h := setupSpawnPipelineHarness(t, spawnPipelinePool(405), []string{targetA, targetB}, queued, time.Minute)

	// An idle on-demand runner registered against repo-b (no demand there).
	h.reconciler.TrackRunner(orchestrator.RunnerStatus{
		ID:        "cnt-idle-b",
		Name:      "idle-on-b",
		PoolName:  "spawn-pipeline-405",
		PoolID:    405,
		State:     "running",
		SpawnedAt: time.Now().UTC(),
		OnDemand:  true,
		TargetURL: targetB,
	})

	if err := h.ctrl.Boot(ctx); err != nil {
		t.Fatalf("Boot failed: %v", err)
	}

	if h.spawnCount() != 1 {
		t.Fatalf("expected 1 deficit spawn for repo-a demand, got %d", h.spawnCount())
	}
	got := h.spawnTargets()
	if got[targetA] != 1 {
		t.Fatalf("expected the spawn on the demanding target %s, got %v", targetA, got)
	}
}
