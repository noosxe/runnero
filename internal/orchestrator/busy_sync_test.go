package orchestrator_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/noosxe/runnero/internal/db"
	"github.com/noosxe/runnero/internal/orchestrator"
	"github.com/noosxe/runnero/internal/provider"
)

// busySyncHarness wires a controller with an in-memory live-runner map so
// tests can inject tracked runner state deterministically. All pools use
// MinIdleRunners=0 unless a test needs boot-spawned runners, so no implicit
// spawns or drains interfere with the assertions.
type busySyncHarness struct {
	ctrl        *orchestrator.PoolController
	engine      *orchestrator.MockContainerProvider
	reconciler  *orchestrator.Reconciler
	mockProv    *mockGitProvider
	liveMu      sync.Mutex
	liveRunners map[string]orchestrator.RunnerStatus
	terminated  []string
}

func newBusySyncHarness(t *testing.T, pool db.RunnerPool) *busySyncHarness {
	t.Helper()
	h := &busySyncHarness{
		mockProv:    &mockGitProvider{},
		liveRunners: make(map[string]orchestrator.RunnerStatus),
	}
	resolver := &mockGitProviderResolver{
		providers: map[int64]provider.GitProvider{10: h.mockProv},
	}
	h.engine = &orchestrator.MockContainerProvider{
		AuditRunnersFn: func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
			h.liveMu.Lock()
			defer h.liveMu.Unlock()
			res := make([]orchestrator.RunnerStatus, 0, len(h.liveRunners))
			for _, r := range h.liveRunners {
				res = append(res, r)
			}
			return res, nil
		},
		TerminateRunnerFn: func(ctx context.Context, containerID string) error {
			h.liveMu.Lock()
			delete(h.liveRunners, containerID)
			h.terminated = append(h.terminated, containerID)
			h.liveMu.Unlock()
			return nil
		},
		SpawnRunnerFn: func(ctx context.Context, cfg orchestrator.RunnerConfig) (string, error) {
			h.liveMu.Lock()
			id := fmt.Sprintf("c-%d", len(h.liveRunners)+1)
			h.liveRunners[id] = orchestrator.RunnerStatus{
				ID:        id,
				Name:      cfg.Name,
				PoolName:  cfg.PoolName,
				State:     "running",
				SpawnedAt: time.Now().UTC(),
			}
			h.liveMu.Unlock()
			return id, nil
		},
		PingFn: func(ctx context.Context) error { return nil },
	}
	h.reconciler = orchestrator.NewReconciler(h.engine)
	h.ctrl = orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:               &mockPoolRepo{pools: []db.RunnerPool{pool}},
		ContainerEngine:  h.engine,
		ProviderResolver: resolver,
		Reconciler:       h.reconciler,
		Interval:         time.Hour,
	})
	return h
}

// injectRunner registers a runner as both engine-live and reconciler-tracked.
func (h *busySyncHarness) injectRunner(pool db.RunnerPool, name string, busy bool) {
	h.liveMu.Lock()
	defer h.liveMu.Unlock()
	status := orchestrator.RunnerStatus{
		ID:        "container-" + name,
		Name:      name,
		PoolName:  pool.Name,
		PoolID:    pool.ID,
		State:     "running",
		IsBusy:    busy,
		SpawnedAt: time.Now().UTC(),
	}
	h.liveRunners[status.ID] = status
	h.reconciler.TrackRunner(status)
}

func (h *busySyncHarness) trackedByName(pool db.RunnerPool) map[string]orchestrator.RunnerStatus {
	byName := make(map[string]orchestrator.RunnerStatus)
	for _, r := range h.reconciler.TrackedPoolRunners(pool.ID) {
		byName[r.Name] = r
	}
	return byName
}

// busySyncPool returns a baseline webhook pool with the given idle/concurrency targets.
func busySyncPool(name string, minIdle, maxConcurrency int) db.RunnerPool {
	return db.RunnerPool{
		ID:             1,
		Name:           name,
		Provider:       "github",
		RepositoryUrl:  "https://github.com/my-org/my-repo",
		Scope:          "repo",
		AuthProfileID:  10,
		MinIdleRunners: int64(minIdle),
		MaxConcurrency: int64(maxConcurrency),
		Labels:         `["self-hosted","linux"]`,
		RunnerImage:    "ghcr.io/noosxe/runnero:latest",
	}
}

// TestBusySync_MarksTrackedRunnerBusyBeforeClassification verifies the audit-cycle
// sync flips IsBusy from the provider listing (docs/19 §2.3), so a runner that
// picked up a job without a webhook delivery is no longer classified idle.
func TestBusySync_MarksTrackedRunnerBusyBeforeClassification(t *testing.T) {
	ctx := context.Background()
	pool := busySyncPool("busy-sync-classify", 1, 5)
	h := newBusySyncHarness(t, pool)

	if err := h.ctrl.Boot(ctx); err != nil {
		t.Fatalf("boot failed: %v", err)
	}

	runners := h.reconciler.TrackedPoolRunners(pool.ID)
	if len(runners) != 1 {
		t.Fatalf("expected 1 tracked runner after boot, got %d", len(runners))
	}
	runnerName := runners[0].Name

	h.mockProv.remoteRunners = []provider.RemoteRunnerStatus{
		{Name: runnerName, Busy: true, Online: true},
	}

	if err := h.ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if h.mockProv.listCalls == 0 {
		t.Fatal("expected ListRunners to be called during reconcile")
	}

	byName := h.trackedByName(pool)
	r, ok := byName[runnerName]
	if !ok {
		t.Fatalf("boot runner %q disappeared from tracking", runnerName)
	}
	if !r.IsBusy {
		t.Errorf("expected runner %q to be marked busy by the sync, got %+v", runnerName, r)
	}
}

// TestBusySync_BusyRunnerNotDrainedByScaleToZero is the regression test for the
// reported bug: a runner that is actually executing a job (busy at the forge but
// never marked locally due to a missed webhook) must NOT be drained when the
// pool scales to zero (docs/19 §1, §2.3).
func TestBusySync_BusyRunnerNotDrainedByScaleToZero(t *testing.T) {
	ctx := context.Background()
	pool := busySyncPool("busy-sync-drain", 0, 5) // scale-to-zero: idle runners drained
	h := newBusySyncHarness(t, pool)

	if err := h.ctrl.Boot(ctx); err != nil {
		t.Fatalf("boot failed: %v", err)
	}
	h.injectRunner(pool, "runnero-midjob", false)

	// Runner reports busy at the forge.
	h.mockProv.remoteRunners = []provider.RemoteRunnerStatus{
		{Name: "runnero-midjob", Busy: true, Online: true},
	}
	if err := h.ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	runners := h.reconciler.TrackedPoolRunners(pool.ID)
	if len(runners) != 1 || !runners[0].IsBusy {
		t.Fatalf("mid-job runner must survive scale-to-zero drain and be busy, got %+v", runners)
	}
	if len(h.terminated) != 0 {
		t.Fatalf("mid-job runner must not be terminated, terminated=%v", h.terminated)
	}

	// Job completes at the forge; the next cycle converges and drains the now-idle runner.
	h.mockProv.remoteRunners = []provider.RemoteRunnerStatus{
		{Name: "runnero-midjob", Busy: false, Online: true},
	}
	if err := h.ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("second reconcile failed: %v", err)
	}
	if got := h.reconciler.TrackedPoolRunners(pool.ID); len(got) != 0 {
		t.Fatalf("idle runner should be drained after job completion, got %+v", got)
	}
	if len(h.terminated) != 1 || h.terminated[0] != "container-runnero-midjob" {
		t.Fatalf("expected exactly the idle runner terminated, got %v", h.terminated)
	}
}

// TestBusySync_OfflineGuardAndAbsentNames verifies state preservation rules:
// offline runners and names absent from the listing keep their current state
// (docs/19 §2.3). Both injected runners are busy so nothing is drained,
// keeping the guards the only state-affecting paths.
func TestBusySync_OfflineGuardAndAbsentNames(t *testing.T) {
	ctx := context.Background()
	pool := busySyncPool("busy-sync-offline", 0, 5)
	h := newBusySyncHarness(t, pool)

	if err := h.ctrl.Boot(ctx); err != nil {
		t.Fatalf("boot failed: %v", err)
	}
	h.injectRunner(pool, "runnero-offline-busy", true)
	h.injectRunner(pool, "runnero-absent", true)

	h.mockProv.remoteRunners = []provider.RemoteRunnerStatus{
		// Online=false: must NOT clobber the webhook-set busy state...
		{Name: "runnero-offline-busy", Busy: false, Online: false},
		// runnero-absent deliberately missing from the listing.
	}
	if err := h.ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	byName := h.trackedByName(pool)
	if !byName["runnero-offline-busy"].IsBusy {
		t.Error("offline guard: busy state must be preserved for offline runners")
	}
	if !byName["runnero-absent"].IsBusy {
		t.Error("absent-name guard: state must be untouched for runners missing from the listing")
	}
}

// TestBusySync_ListerErrorFailsOpen verifies a listing failure never blocks the
// reconcile and preserves last-known state (docs/19 §2.3).
func TestBusySync_ListerErrorFailsOpen(t *testing.T) {
	ctx := context.Background()
	pool := busySyncPool("busy-sync-err", 0, 5)
	h := newBusySyncHarness(t, pool)

	if err := h.ctrl.Boot(ctx); err != nil {
		t.Fatalf("boot failed: %v", err)
	}
	h.injectRunner(pool, "runnero-keep", true)

	h.mockProv.listErr = errors.New("forge API unavailable")
	if err := h.ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile must not fail on lister errors: %v", err)
	}

	runners := h.reconciler.TrackedPoolRunners(pool.ID)
	if len(runners) != 1 || !runners[0].IsBusy {
		t.Fatalf("fail-open: last-known busy state must be preserved, got %+v", runners)
	}
	if len(h.terminated) != 0 {
		t.Fatalf("fail-open: no runner should be terminated on lister errors, got %v", h.terminated)
	}
}

// plainGitProvider implements provider.GitProvider WITHOUT RunnerLister —
// used to assert the optional-interface pattern keeps such providers untouched.
type plainGitProvider struct{}

func (p *plainGitProvider) GetRegistrationToken(ctx context.Context, scope provider.RegistrationScope, targetURL string) (string, error) {
	return "reg-token-mock", nil
}

func (p *plainGitProvider) ValidateCredentials(ctx context.Context) error { return nil }

func (p *plainGitProvider) ScalingMode() provider.ScalingMode { return provider.ScalingWebhook }

func (p *plainGitProvider) PollQueuedJobs(ctx context.Context, targetURL string) (int, error) {
	return 0, nil
}

func (p *plainGitProvider) DiscoverOrganizations(ctx context.Context) ([]provider.DiscoveredTarget, error) {
	return nil, nil
}

func (p *plainGitProvider) DiscoverRepositories(ctx context.Context) ([]provider.DiscoveredTarget, error) {
	return nil, nil
}

// TestBusySync_ProviderWithoutListerUntouched verifies providers that do not
// implement RunnerLister behave exactly as before (docs/19 §2.1): the sync is
// skipped entirely, so state and classification are unchanged.
func TestBusySync_ProviderWithoutListerUntouched(t *testing.T) {
	ctx := context.Background()
	pool := busySyncPool("busy-sync-plain", 1, 5)
	h := newBusySyncHarness(t, pool)
	// Swap in a provider without RunnerLister support.
	h.ctrl = orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:               &mockPoolRepo{pools: []db.RunnerPool{pool}},
		ContainerEngine:  h.engine,
		ProviderResolver: &mockGitProviderResolver{providers: map[int64]provider.GitProvider{10: &plainGitProvider{}}},
		Reconciler:       h.reconciler,
		Interval:         time.Hour,
	})

	if err := h.ctrl.Boot(ctx); err != nil {
		t.Fatalf("boot failed: %v", err)
	}

	if err := h.ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	runners := h.reconciler.TrackedPoolRunners(pool.ID)
	if len(runners) != 1 {
		t.Fatalf("expected the boot runner to remain tracked, got %d", len(runners))
	}
	if runners[0].IsBusy {
		t.Error("provider without RunnerLister must leave busy state untouched")
	}
	active, idle := h.ctrl.PoolStats(pool.ID)
	if active != 0 || idle != 1 {
		t.Errorf("expected untouched idle classification (active=0 idle=1), got active=%d idle=%d", active, idle)
	}
}
