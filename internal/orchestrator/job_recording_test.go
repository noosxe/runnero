package orchestrator_test

import (
	"context"
	"testing"
	"time"

	"github.com/noosxe/runnero/internal/db"
	"github.com/noosxe/runnero/internal/orchestrator"
	"github.com/noosxe/runnero/internal/provider"
)

// jobRecHarness extends the busy-sync harness with a recording mock wired into
// the controller (docs/21 Phase 1 verification).
type jobRecHarness struct {
	*busySyncHarness
	rec *mockJobRecorder
}

func newJobRecHarness(t *testing.T, pool db.RunnerPool) *jobRecHarness {
	t.Helper()
	h := &jobRecHarness{busySyncHarness: newBusySyncHarness(t, pool), rec: &mockJobRecorder{}}

	// Re-wire the controller with the recorder attached. The busy-sync harness
	// builds its own controller, so swap it via the exported constructor with
	// identical options plus the recorder.
	resolver := &mockGitProviderResolver{
		providers: map[int64]provider.GitProvider{pool.AuthProfileID: h.mockProv},
	}
	h.ctrl = orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:               &mockPoolRepo{pools: []db.RunnerPool{pool}},
		ContainerEngine:  h.engine,
		ProviderResolver: resolver,
		Reconciler:       h.reconciler,
		JobRecorder:      h.rec,
		Interval:         time.Hour,
	})
	return h
}

func (h *jobRecHarness) addTrackedRunner(pool db.RunnerPool, name string, busy bool) {
	status := orchestrator.RunnerStatus{
		ID:        "c-" + name,
		Name:      name,
		PoolName:  pool.Name,
		PoolID:    pool.ID,
		State:     "running",
		IsBusy:    busy,
		SpawnedAt: time.Now().UTC(),
	}
	h.liveMu.Lock()
	h.liveRunners[status.ID] = status
	h.liveMu.Unlock()
	h.reconciler.TrackRunner(status)
}

// TestJobRecording_OpenOnIdleToBusyTransition verifies a runner observed
// flipping idle→busy by the busy-state sync opens exactly one job row
// (docs/21 §5.2) — the webhookless-safe primary recording path.
func TestJobRecording_OpenOnIdleToBusyTransition(t *testing.T) {
	ctx := context.Background()
	pool := busySyncPool("job-rec-pool", 1, 5)
	h := newJobRecHarness(t, pool)

	if err := h.ctrl.Boot(ctx); err != nil {
		t.Fatalf("boot failed: %v", err)
	}

	runners := h.reconciler.TrackedPoolRunners(pool.ID)
	if len(runners) != 1 {
		t.Fatalf("expected 1 tracked runner after boot, got %d", len(runners))
	}
	runnerName := runners[0].Name

	// Boot provisioned an idle standby; first listing shows it idle — no opens.
	h.mockProv.remoteRunners = []provider.RemoteRunnerStatus{
		{Name: runnerName, Busy: false, Online: true},
	}
	if err := h.ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if len(h.rec.opens) != 0 {
		t.Fatalf("idle runner must not open a job row, got %d opens", len(h.rec.opens))
	}

	// Runner picks up a job: idle→busy → open.
	h.mockProv.remoteRunners = []provider.RemoteRunnerStatus{
		{Name: runnerName, Busy: true, Online: true},
	}
	if err := h.ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if len(h.rec.opens) != 1 || h.rec.opens[0].runnerName != runnerName {
		t.Fatalf("expected 1 open for %q, got %+v", runnerName, h.rec.opens)
	}
	if h.rec.opens[0].poolID != pool.ID {
		t.Fatalf("open used pool id %d, want %d", h.rec.opens[0].poolID, pool.ID)
	}

	// Repeated busy listings must not duplicate opens (one-open-row invariant).
	if err := h.ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("second reconcile failed: %v", err)
	}
	if len(h.rec.opens) != 1 {
		t.Fatalf("busy runner re-listed must not re-open, got %d opens", len(h.rec.opens))
	}

	// Job completes: busy→idle → close as completed.
	h.mockProv.remoteRunners = []provider.RemoteRunnerStatus{
		{Name: runnerName, Busy: false, Online: true},
	}
	if err := h.ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("final reconcile failed: %v", err)
	}
	if len(h.rec.closes) != 1 || h.rec.closes[0].status != "completed" || h.rec.closes[0].runnerName != runnerName {
		t.Fatalf("expected 1 close(completed) for %q, got %+v", runnerName, h.rec.closes)
	}
}

// TestJobRecording_BootRecoveryClosesInterruptedRows verifies the docs/21 §5.4
// boot sequence force-closes rows left open by a previous supervisor lifetime.
func TestJobRecording_BootRecoveryClosesInterruptedRows(t *testing.T) {
	ctx := context.Background()
	pool := busySyncPool("job-rec-boot", 0, 5)
	h := newJobRecHarness(t, pool)
	h.rec.interrupted = 2

	if err := h.ctrl.Boot(ctx); err != nil {
		t.Fatalf("boot failed: %v", err)
	}
	// The recorder mock returns the seeded count on first invocation.
	if h.rec.interrupted != 0 {
		t.Fatalf("boot must consume the interrupted-row count, got %d", h.rec.interrupted)
	}
}

// TestJobRecording_CloseOnContainerDeath verifies the reap path closes a busy
// runner's open row: clean exit → 'completed', ungraceful → 'interrupted'
// (docs/21 §5.2).
func TestJobRecording_CloseOnContainerDeath(t *testing.T) {
	ctx := context.Background()
	pool := busySyncPool("job-rec-death", 0, 5)
	h := newJobRecHarness(t, pool)

	if err := h.ctrl.Boot(ctx); err != nil {
		t.Fatalf("boot failed: %v", err)
	}

	h.addTrackedRunner(pool, "runnero-death-1", false)
	h.mockProv.remoteRunners = []provider.RemoteRunnerStatus{
		{Name: "runnero-death-1", Busy: true, Online: true},
	}
	if err := h.ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if len(h.rec.opens) != 1 {
		t.Fatalf("expected 1 open for the busy runner, got %+v", h.rec.opens)
	}

	// Container dies ungracefully (exit code 137 = OOM/kill): reap closes the
	// row as 'interrupted' and the die-event handler replenishes the pool.
	if err := h.ctrl.HandleContainerEvent(ctx, orchestrator.ContainerEvent{
		ContainerID: "c-runnero-death-1",
		PoolName:    pool.Name,
		Action:      "die",
		ExitCode:    137,
	}); err != nil {
		t.Fatalf("HandleContainerEvent failed: %v", err)
	}
	if len(h.rec.closes) != 1 || h.rec.closes[0].status != "interrupted" {
		t.Fatalf("expected 1 close(interrupted), got %+v", h.rec.closes)
	}
}

// TestJobRecording_CleanExitClosesCompleted verifies exit code 0 closes the
// row as 'completed'.
func TestJobRecording_CleanExitClosesCompleted(t *testing.T) {
	ctx := context.Background()
	pool := busySyncPool("job-rec-clean", 0, 5)
	h := newJobRecHarness(t, pool)

	if err := h.ctrl.Boot(ctx); err != nil {
		t.Fatalf("boot failed: %v", err)
	}

	h.addTrackedRunner(pool, "runnero-clean-1", false)
	h.mockProv.remoteRunners = []provider.RemoteRunnerStatus{
		{Name: "runnero-clean-1", Busy: true, Online: true},
	}
	if err := h.ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	if err := h.ctrl.HandleContainerEvent(ctx, orchestrator.ContainerEvent{
		ContainerID: "c-runnero-clean-1",
		PoolName:    pool.Name,
		Action:      "die",
		ExitCode:    0,
	}); err != nil {
		t.Fatalf("HandleContainerEvent failed: %v", err)
	}
	if len(h.rec.closes) != 1 || h.rec.closes[0].status != "completed" {
		t.Fatalf("expected 1 close(completed), got %+v", h.rec.closes)
	}
}

// TestJobRecording_LifetimeKillIdleStandbyRecordsNothing verifies the docs/21
// §5.2 guard end to end: the max-lifetime kill switch still force-terminates an
// idle standby that never ran a job, but records no timeout row - the runner
// has no open job row, so kill-switch churn must not fabricate job history.
func TestJobRecording_LifetimeKillIdleStandbyRecordsNothing(t *testing.T) {
	ctx := context.Background()
	pool := busySyncPool("job-rec-pool", 0, 5)
	pool.MaxRunnerLifetimeSeconds = 7200
	h := newJobRecHarness(t, pool)
	if err := h.ctrl.Boot(ctx); err != nil {
		t.Fatalf("boot failed: %v", err)
	}

	// An idle standby spawned 3h ago: past the 2h lifetime limit.
	status := orchestrator.RunnerStatus{
		ID:        "c-runnero-old-standby",
		Name:      "runnero-old-standby",
		PoolName:  pool.Name,
		State:     "running",
		IsBusy:    false,
		SpawnedAt: time.Now().UTC().Add(-3 * time.Hour),
	}
	h.liveMu.Lock()
	h.liveRunners[status.ID] = status
	h.liveMu.Unlock()
	h.reconciler.TrackRunner(status)

	if err := h.ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	if len(h.rec.records) != 0 {
		t.Fatalf("lifetime kill of an idle standby must record no job row, got %+v", h.rec.records)
	}
}

// TestJobRecording_LifetimeKillBusyRunnerRecordsTimeout verifies the guard's
// other half: force-terminating a runner that was mid-job still closes its
// open row as a timeout (docs/21 §5.2).
func TestJobRecording_LifetimeKillBusyRunnerRecordsTimeout(t *testing.T) {
	ctx := context.Background()
	pool := busySyncPool("job-rec-pool", 0, 5)
	pool.MaxRunnerLifetimeSeconds = 7200
	h := newJobRecHarness(t, pool)
	if err := h.ctrl.Boot(ctx); err != nil {
		t.Fatalf("boot failed: %v", err)
	}

	status := orchestrator.RunnerStatus{
		ID:        "c-runnero-old-busy",
		Name:      "runnero-old-busy",
		PoolName:  pool.Name,
		State:     "running",
		IsBusy:    true,
		SpawnedAt: time.Now().UTC().Add(-3 * time.Hour),
	}
	h.liveMu.Lock()
	h.liveRunners[status.ID] = status
	h.liveMu.Unlock()
	h.reconciler.TrackRunner(status)
	// The busy-state sync opened this runner's job row on idle→busy.
	if err := h.rec.OpenTransitionJob(ctx, pool.ID, "runnero-old-busy", time.Now().UTC().Add(-2*time.Hour)); err != nil {
		t.Fatalf("OpenTransitionJob failed: %v", err)
	}

	if err := h.ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	if len(h.rec.records) != 1 {
		t.Fatalf("lifetime kill of a mid-job runner must record a timeout, got %+v", h.rec.records)
	}
	if h.rec.records[0].runnerName != "runnero-old-busy" {
		t.Fatalf("timeout recorded for %q, want runnero-old-busy", h.rec.records[0].runnerName)
	}
}
