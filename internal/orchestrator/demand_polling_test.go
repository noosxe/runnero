package orchestrator_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/noosxe/runnero/internal/db"
	"github.com/noosxe/runnero/internal/orchestrator"
	"github.com/noosxe/runnero/internal/provider"
)

// capturingGitProvider records the last PollTarget seen by demand polling
// (docs/24 §5.3) while delegating to the shared mock.
type capturingGitProvider struct {
	*mockGitProvider
	mu       sync.Mutex
	lastPoll provider.PollTarget
}

func (c *capturingGitProvider) PollQueuedJobs(ctx context.Context, target provider.PollTarget) (int, error) {
	c.mu.Lock()
	c.lastPoll = target
	c.mu.Unlock()
	return c.mockGitProvider.PollQueuedJobs(ctx, target)
}

func (c *capturingGitProvider) pollTarget() provider.PollTarget {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastPoll
}

// newDemandPollingHarness wires a single-pool controller with a capturing mock
// provider for demand-polling gate tests (docs/24 §5).
type demandPollingHarness struct {
	ctrl       *orchestrator.PoolController
	reconciler *orchestrator.Reconciler
	prov       *capturingGitProvider
	spawned    int
	spawnMu    sync.Mutex
}

func setupDemandPollingHarness(t *testing.T, pool db.RunnerPool, scalingMode provider.ScalingMode, queued int, pollErr error) *demandPollingHarness {
	t.Helper()

	gitProv := &mockGitProvider{
		scalingMode: scalingMode,
		queuedJobs:  queued,
		pollErr:     pollErr,
	}
	prov := &capturingGitProvider{mockGitProvider: gitProv}
	resolver := &mockGitProviderResolver{
		providers: map[int64]provider.GitProvider{10: prov},
	}

	h := &demandPollingHarness{prov: prov}

	var reconciler *orchestrator.Reconciler
	mockEngine := &orchestrator.MockContainerProvider{
		SpawnRunnerFn: func(ctx context.Context, config orchestrator.RunnerConfig) (string, error) {
			h.spawnMu.Lock()
			h.spawned++
			n := h.spawned
			h.spawnMu.Unlock()
			return fmt.Sprintf("runner-%d", n), nil
		},
		AuditRunnersFn: func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
			if reconciler == nil {
				return nil, nil
			}
			return reconciler.TrackedPoolRunners(pool.ID), nil
		},
		PingFn: func(ctx context.Context) error { return nil },
	}
	reconciler = orchestrator.NewReconciler(mockEngine)
	h.reconciler = reconciler

	h.ctrl = orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:               &mockPoolRepo{pools: []db.RunnerPool{pool}},
		ContainerEngine:  mockEngine,
		ProviderResolver: resolver,
		Reconciler:       reconciler,
		GlobalMaxRunners: 10,
		Interval:         time.Hour,
	})
	return h
}

func (h *demandPollingHarness) spawnCount() int {
	h.spawnMu.Lock()
	defer h.spawnMu.Unlock()
	return h.spawned
}

func githubPollPool(id int64, fallback bool, interval int64, scope string) db.RunnerPool {
	return db.RunnerPool{
		ID:                  id,
		Name:                "github-poll",
		Provider:            "github",
		RepositoryUrl:       "https://github.com/acme/repo",
		Scope:               scope,
		AuthProfileID:       10,
		MinIdleRunners:      0,
		MaxConcurrency:      4,
		Labels:              `["self-hosted","linux"]`,
		RunnerImage:         "ghcr.io/noosxe/runnero:latest",
		PollFallback:        fallback,
		PollIntervalSeconds: interval,
	}
}

// A GitHub pool without poll_fallback must never poll (webhook-only scaling,
// docs/24 §5.1) — the pre-RUN-145 behavior is preserved.
func TestPoolController_PollFallbackDisabledNeverPolls(t *testing.T) {
	ctx := context.Background()
	pool := githubPollPool(300, false, 0, "repo")
	h := setupDemandPollingHarness(t, pool, provider.ScalingWebhook, 3, nil)

	if err := h.ctrl.Boot(ctx); err != nil {
		t.Fatalf("Boot failed: %v", err)
	}

	if h.prov.pollCalls != 0 {
		t.Errorf("expected no poll calls for fallback-disabled pool, got %d", h.prov.pollCalls)
	}
	if h.spawnCount() != 0 {
		t.Errorf("expected no demand spawns for fallback-disabled scale-to-zero pool, got %d", h.spawnCount())
	}
}

// A GitHub pool with poll_fallback polls for queued jobs and provisions
// deficit runners as on-demand (docs/24 §5.1/§5.6), propagating scope and
// labels to the provider query (docs/24 §5.3).
func TestPoolController_PollFallbackEnabledSpawnsForQueued(t *testing.T) {
	ctx := context.Background()
	pool := githubPollPool(301, true, 30, "repo")
	h := setupDemandPollingHarness(t, pool, provider.ScalingWebhook, 2, nil)

	if err := h.ctrl.Boot(ctx); err != nil {
		t.Fatalf("Boot failed: %v", err)
	}

	if h.prov.pollCalls != 1 {
		t.Fatalf("expected 1 poll call on boot, got %d", h.prov.pollCalls)
	}
	if h.spawnCount() != 2 {
		t.Fatalf("expected 2 on-demand spawns from 2 queued jobs, got %d", h.spawnCount())
	}

	target := h.prov.pollTarget()
	if target.Scope != provider.ScopeRepo {
		t.Errorf("expected repo scope on poll target, got %q", target.Scope)
	}
	if target.URL != pool.RepositoryUrl {
		t.Errorf("expected pool target URL %q, got %q", pool.RepositoryUrl, target.URL)
	}
	if !strings.Contains(target.Labels, "self-hosted") {
		t.Errorf("expected pool labels on poll target, got %q", target.Labels)
	}

	tracked := h.reconciler.TrackedPoolRunners(pool.ID)
	if len(tracked) != 2 {
		t.Fatalf("expected 2 tracked runners, got %d", len(tracked))
	}
	for _, r := range tracked {
		if !r.OnDemand {
			t.Errorf("deficit-spawned runner %s must be on-demand (docs/24 §5.6)", r.ID)
		}
	}
}

// The per-pool interval throttles subsequent audit cycles (docs/24 §5.4):
// the pool polls on boot, then skips cycles until the interval elapses.
func TestPoolController_PollFallbackThrottledByInterval(t *testing.T) {
	ctx := context.Background()
	pool := db.RunnerPool{
		ID:                  302,
		Name:                "github-throttled",
		Provider:            "github",
		RepositoryUrl:       "https://github.com/acme/repo",
		Scope:               "repo",
		AuthProfileID:       10,
		MinIdleRunners:      1,
		MaxConcurrency:      4,
		Labels:              `["self-hosted"]`,
		RunnerImage:         "ghcr.io/noosxe/runnero:latest",
		PollFallback:        true,
		PollIntervalSeconds: 3600,
	}
	h := setupDemandPollingHarness(t, pool, provider.ScalingWebhook, 0, nil)

	if err := h.ctrl.Boot(ctx); err != nil {
		t.Fatalf("Boot failed: %v", err)
	}
	if h.prov.pollCalls != 1 {
		t.Fatalf("expected 1 poll call on boot, got %d", h.prov.pollCalls)
	}

	// New demand arrives, but the interval has not elapsed: no second poll,
	// no additional spawns within the same cycle.
	h.prov.queuedJobs = 5
	if err := h.ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	if h.prov.pollCalls != 1 {
		t.Errorf("expected throttled pool to skip the second cycle, got %d poll calls", h.prov.pollCalls)
	}
	if h.spawnCount() != 1 {
		t.Errorf("expected only the min_idle standby, got %d spawns", h.spawnCount())
	}
}

// A zero interval means "no throttle" (legacy/test rows, docs/24 §5.4):
// every audit cycle polls.
func TestPoolController_PollFallbackZeroIntervalPollsEveryCycle(t *testing.T) {
	ctx := context.Background()
	pool := githubPollPool(303, true, 0, "repo")
	h := setupDemandPollingHarness(t, pool, provider.ScalingWebhook, 0, nil)

	if err := h.ctrl.Boot(ctx); err != nil {
		t.Fatalf("Boot failed: %v", err)
	}
	if err := h.ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	if h.prov.pollCalls != 2 {
		t.Errorf("expected 2 poll calls across two cycles, got %d", h.prov.pollCalls)
	}
}

// Org-scoped GitHub targets are skipped with a diagnostic note, never an
// error-level failure, and never overprovision (docs/24 §5.2/§5.10).
func TestPoolController_PollFallbackOrgScopeSkippedWithDiagnostic(t *testing.T) {
	ctx := context.Background()
	pool := githubPollPool(304, true, 0, "org")
	h := setupDemandPollingHarness(t, pool, provider.ScalingWebhook, 0, provider.ErrPollingScopeUnsupported)

	if err := h.ctrl.Boot(ctx); err != nil {
		t.Fatalf("Boot failed: %v", err)
	}

	if h.spawnCount() != 0 {
		t.Errorf("org-scope poll must not spawn, got %d spawns", h.spawnCount())
	}
	diag := h.ctrl.PoolDiagnostics(pool.ID)
	if !strings.Contains(diag.LastPollError, "poll skipped") {
		t.Errorf("expected skip diagnostic in pool diagnostics, got %q", diag.LastPollError)
	}
	if diag.LastError != "" {
		t.Errorf("scope skip must not set the pool error, got %q", diag.LastError)
	}
}

// A fully-failed poll is surfaced as a poll diagnostic while the pool's own
// error state stays clean (docs/24 §5.9/§5.10).
func TestPoolController_PollFailureSurfacedInDiagnostics(t *testing.T) {
	ctx := context.Background()
	pool := githubPollPool(305, true, 0, "repo")
	h := setupDemandPollingHarness(t, pool, provider.ScalingWebhook, 0, errors.New("rate limit exceeded"))

	if err := h.ctrl.Boot(ctx); err != nil {
		t.Fatalf("Boot failed: %v", err)
	}

	diag := h.ctrl.PoolDiagnostics(pool.ID)
	if diag.LastPollError != "poll failed for all 1 target(s)" {
		t.Errorf("expected full-failure diagnostic, got %q", diag.LastPollError)
	}
	if diag.LastError != "" {
		t.Errorf("poll failure must not set the pool error, got %q", diag.LastError)
	}
}
