package orchestrator_test

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/noosxe/runnero/internal/db"
	"github.com/noosxe/runnero/internal/orchestrator"
	"github.com/noosxe/runnero/internal/provider"
)

// restartHarness models one Docker engine that outlives two supervisor
// lifetimes (docs/03 §7): each lifetime gets a fresh reconciler and
// controller, the engine's containers persist between them.
type restartHarness struct {
	t      *testing.T
	pool   db.RunnerPool
	repo   *mockPoolRepo
	prov   *mockGitProvider
	engine *orchestrator.MockContainerProvider

	mu         sync.Mutex
	live       map[string]orchestrator.RunnerStatus
	terminated []string
}

const restartTarget = "https://github.com/owner/restart-repo"

func newRestartHarness(t *testing.T) *restartHarness {
	t.Helper()
	h := &restartHarness{
		t: t,
		pool: db.RunnerPool{
			ID:             130,
			Name:           "restart-pool",
			Provider:       "github",
			Scope:          "repo",
			AuthProfileID:  1,
			MinIdleRunners: 2,
			MaxConcurrency: 5,
		},
		prov: &mockGitProvider{},
		live: make(map[string]orchestrator.RunnerStatus),
	}
	h.repo = &mockPoolRepo{
		pools:   []db.RunnerPool{h.pool},
		targets: map[int64][]string{h.pool.ID: {restartTarget}},
	}
	h.engine = &orchestrator.MockContainerProvider{
		AuditRunnersFn: func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			out := make([]orchestrator.RunnerStatus, 0, len(h.live))
			for _, r := range h.live {
				out = append(out, r)
			}
			return out, nil
		},
		TerminateRunnerFn: func(ctx context.Context, containerID string) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.terminated = append(h.terminated, containerID)
			delete(h.live, containerID)
			return nil
		},
	}

	spawned := time.Now().Add(-10 * time.Minute).UTC()
	for _, name := range []string{"runnero-restart-pool-busy", "runnero-restart-pool-idle1", "runnero-restart-pool-idle2"} {
		h.live["cnt-"+name] = orchestrator.RunnerStatus{
			ID: "cnt-" + name, Name: name, PoolID: h.pool.ID, PoolName: h.pool.Name,
			State: "running", SpawnedAt: spawned, TargetURL: restartTarget,
		}
	}
	// The forge is the authority on busy state; container listings cannot
	// report it (docs/19 §2.3).
	h.prov.remoteRunners = []provider.RemoteRunnerStatus{
		{Name: "runnero-restart-pool-busy", Busy: true, Online: true, ID: 1},
		{Name: "runnero-restart-pool-idle1", Online: true, ID: 2},
		{Name: "runnero-restart-pool-idle2", Online: true, ID: 3},
	}
	return h
}

// lifetime boots a fresh supervisor lifetime against the shared engine.
func (h *restartHarness) lifetime(rec *mockJobRecorder) (*orchestrator.PoolController, *orchestrator.Reconciler) {
	h.t.Helper()
	reconciler := orchestrator.NewReconciler(h.engine)
	opts := orchestrator.ControllerOptions{
		DB:               h.repo,
		ContainerEngine:  h.engine,
		ProviderResolver: &mockGitProviderResolver{providers: map[int64]provider.GitProvider{1: h.prov}},
		Reconciler:       reconciler,
		GlobalMaxRunners: 10,
		Interval:         time.Hour,
	}
	if rec != nil {
		opts.JobRecorder = rec
	}
	ctrl := orchestrator.NewPoolController(opts)
	if err := ctrl.Boot(context.Background()); err != nil {
		h.t.Fatalf("Boot failed: %v", err)
	}
	return ctrl, reconciler
}

func (h *restartHarness) terminatedIDs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.terminated)
}

func busyOf(r *orchestrator.Reconciler, poolID int64, name string) bool {
	for _, s := range r.TrackedPoolRunners(poolID) {
		if s.Name == name {
			return s.IsBusy
		}
	}
	return false
}

// TestRestart_DetachThenAdopt is the docs/03 §7 restart contract: a detach
// shutdown leaves every runner alive and registered, and the next lifetime
// adopts them - the busy runner stays busy and undrained, the idle runners
// count toward min_idle so nothing is double-spawned, and the busy runner's
// open job row survives boot recovery.
func TestRestart_DetachThenAdopt(t *testing.T) {
	h := newRestartHarness(t)
	ctx := context.Background()

	first, firstRec := h.lifetime(nil)
	if !busyOf(firstRec, h.pool.ID, "runnero-restart-pool-busy") {
		t.Fatal("first lifetime: busy runner not classified busy")
	}
	if err := first.DetachShutdown(ctx); err != nil {
		t.Fatalf("DetachShutdown failed: %v", err)
	}
	if got := h.terminatedIDs(); len(got) != 0 {
		t.Fatalf("detach shutdown terminated runners: %v", got)
	}
	if h.prov.deregCalls != 0 {
		t.Fatalf("detach shutdown deregistered runners: %v", h.prov.deregistered)
	}

	rec := &mockJobRecorder{}
	second, secondRec := h.lifetime(rec)
	for range 3 {
		if err := second.Reconcile(ctx); err != nil {
			t.Fatalf("Reconcile failed: %v", err)
		}
	}

	if n := len(h.engine.SpawnedRunners); n != 0 {
		t.Errorf("adopted runners must cover the pool target, got %d extra spawns", n)
	}
	if got := h.terminatedIDs(); len(got) != 0 {
		t.Errorf("restart must not terminate adopted runners, got %v", got)
	}
	if h.prov.deregCalls != 0 {
		t.Errorf("restart must not deregister adopted runners, got %v", h.prov.deregistered)
	}
	if !busyOf(secondRec, h.pool.ID, "runnero-restart-pool-busy") {
		t.Error("adopted busy runner not re-classified busy from the forge listing")
	}
	if busy, idle := second.PoolStats(h.pool.ID); busy != 1 || idle != 2 {
		t.Errorf("PoolStats after adoption = busy %d idle %d, want 1/2", busy, idle)
	}

	if rec.recoveryCalls != 1 {
		t.Fatalf("boot recovery calls = %d, want 1", rec.recoveryCalls)
	}
	if !slices.Contains(rec.recoveryKeep, db.JobRunnerKey{PoolID: h.pool.ID, RunnerName: "runnero-restart-pool-busy"}) {
		t.Errorf("boot recovery must keep the adopted busy runner's job row, keep = %+v", rec.recoveryKeep)
	}
	if len(rec.recoveryKeep) != 3 {
		t.Errorf("boot recovery keep = %+v, want all 3 adopted runners", rec.recoveryKeep)
	}
}

// TestRestart_BusyVetoHoldsWithoutForgeListing covers the RUN-182 startup
// window: adopted runners start out locally idle, and if the forge listing
// is unavailable a scale-down could pick the mid-job runner. The provider's
// busy veto must preserve it and re-mark it busy.
func TestRestart_BusyVetoHoldsWithoutForgeListing(t *testing.T) {
	h := newRestartHarness(t)
	h.pool.MinIdleRunners = 0
	h.repo.pools = []db.RunnerPool{h.pool}
	h.prov.listErr = context.DeadlineExceeded
	h.prov.deregErrByName = map[string]error{"runnero-restart-pool-busy": provider.ErrRunnerBusy}

	_, reconciler := h.lifetime(nil)

	if h.prov.deregCalls == 0 {
		t.Fatal("scale-down never consulted the provider: the veto path was not exercised")
	}
	for _, id := range h.terminatedIDs() {
		if id == "cnt-runnero-restart-pool-busy" {
			t.Fatal("busy runner terminated despite the provider's busy veto")
		}
	}
	if !busyOf(reconciler, h.pool.ID, "runnero-restart-pool-busy") {
		t.Error("vetoed runner must be re-marked busy")
	}
}

// TestRestart_AdoptedRunnerKeepsLifetimeLimit verifies the lifetime kill
// switch still covers adopted runners: adoption anchors BusySince at spawn
// (docs/23 §4.4.1), so a job that started before the restart is still
// bounded by max_runner_lifetime_seconds.
func TestRestart_AdoptedRunnerKeepsLifetimeLimit(t *testing.T) {
	h := newRestartHarness(t)
	h.pool.MaxRunnerLifetimeSeconds = 60 // containers were spawned 10 minutes ago
	h.repo.pools = []db.RunnerPool{h.pool}

	ctrl, _ := h.lifetime(nil)
	if err := ctrl.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	if !slices.Contains(h.terminatedIDs(), "cnt-runnero-restart-pool-busy") {
		t.Errorf("adopted busy runner past its lifetime was not terminated, terminated = %v", h.terminatedIDs())
	}
}

// TestRestart_JobFinishedDuringDowntime covers a busy runner whose job ended
// while the supervisor was down: its exited container is adopted, its job row
// is kept through boot recovery, and the first reconcile reaps it and closes
// the row from the exit code instead of as 'interrupted'.
func TestRestart_JobFinishedDuringDowntime(t *testing.T) {
	h := newRestartHarness(t)
	h.mu.Lock()
	r := h.live["cnt-runnero-restart-pool-busy"]
	r.State = "exited"
	r.ExitCode = 0
	h.live[r.ID] = r
	h.mu.Unlock()
	h.prov.remoteRunners = h.prov.remoteRunners[1:] // ephemeral runner deregistered itself

	rec := &mockJobRecorder{}
	ctrl, _ := h.lifetime(rec)
	if !slices.Contains(rec.recoveryKeep, db.JobRunnerKey{PoolID: h.pool.ID, RunnerName: "runnero-restart-pool-busy"}) {
		t.Fatalf("boot recovery must keep the exited runner's row for the reap path, keep = %+v", rec.recoveryKeep)
	}
	if err := ctrl.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	closed := false
	for _, c := range rec.closes {
		if c.runnerName == "runnero-restart-pool-busy" && c.status == "completed" {
			closed = true
		}
	}
	if !closed {
		t.Errorf("reap must close the finished job as completed, closes = %+v", rec.closes)
	}
}
