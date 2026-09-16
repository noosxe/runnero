package orchestrator_test

// Graceful pool drain tests (RUN-127, docs/25): per-delete drain mode on
// pool delete — idle runners terminated immediately in both modes, busy
// runners spared under graceful drain and bounded by the lifetime backstop,
// restart/out-of-band deletes converging gracefully via the removed-pool
// fallback.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/noosxe/runnero/internal/orchestrator"
)

// newDrainHarness builds a DB-less controller wired to a mock engine and
// reconciler — enough for drainPool/DrainPool/checkHungRunners, none of
// which require a pool repository.
func newDrainHarness(t *testing.T) (*orchestrator.PoolController, *orchestrator.MockContainerProvider, *orchestrator.Reconciler) {
	t.Helper()
	return newDrainHarnessWithRepo(t, nil)
}

// newDrainHarnessWithRepo wires the controller to a pool repository so tests
// can exercise the ownership gate (RUN-239, docs/33 §3.2).
func newDrainHarnessWithRepo(t *testing.T, repo orchestrator.PoolRepository) (*orchestrator.PoolController, *orchestrator.MockContainerProvider, *orchestrator.Reconciler) {
	t.Helper()
	engine := orchestrator.NewMockContainerProvider()
	reconciler := orchestrator.NewReconciler(engine)
	ctrl := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		ContainerEngine: engine,
		Reconciler:      reconciler,
		DB:              repo,
	})
	return ctrl, engine, reconciler
}

func drainRunner(id, name string, poolID int64, busy bool, busySince time.Time) orchestrator.RunnerStatus {
	return orchestrator.RunnerStatus{
		ID:        id,
		Name:      name,
		PoolName:  "drain-pool",
		PoolID:    poolID,
		State:     "running",
		IsBusy:    busy,
		BusySince: busySince,
		SpawnedAt: time.Now().UTC().Add(-time.Minute),
	}
}

// TestDrainPool_HardModePreservesPreRun127Behavior regression-locks the hard
// path: every running runner — busy included — is deregistered, terminated,
// and untracked (docs/25 §4.2).
func TestDrainPool_HardModePreservesPreRun127Behavior(t *testing.T) {
	ctrl, engine, rec := newDrainHarness(t)
	ctx := context.Background()

	now := time.Now().UTC()
	rec.TrackRunner(drainRunner("c-idle", "runnero-drain-idle", 900, false, time.Time{}))
	rec.TrackRunner(drainRunner("c-busy", "runnero-drain-busy", 900, true, now.Add(-time.Hour)))

	ctrl.DrainPool(ctx, 900, "drain-pool", 0, false)

	if len(engine.TerminatedIDs) != 2 {
		t.Fatalf("hard drain must terminate both runners, got %v", engine.TerminatedIDs)
	}
	if got := rec.TrackedPoolRunners(900); len(got) != 0 {
		t.Fatalf("hard drain must untrack everything, got %+v", got)
	}
}

// TestDrainPool_GracefulSparesBusyTerminatesIdle verifies the core split
// (docs/25 §4.2): idle runners go immediately, busy runners are left
// untouched — still tracked so the audit reap path can pick them up on
// ephemeral exit.
func TestDrainPool_GracefulSparesBusyTerminatesIdle(t *testing.T) {
	ctrl, engine, rec := newDrainHarness(t)
	ctx := context.Background()

	now := time.Now().UTC()
	rec.TrackRunner(drainRunner("c-idle", "runnero-drain-idle", 901, false, time.Time{}))
	rec.TrackRunner(drainRunner("c-busy", "runnero-drain-busy", 901, true, now.Add(-time.Hour)))

	ctrl.DrainPool(ctx, 901, "drain-pool", 0, true)

	if len(engine.TerminatedIDs) != 1 || engine.TerminatedIDs[0] != "c-idle" {
		t.Fatalf("graceful drain must terminate only the idle runner, got %v", engine.TerminatedIDs)
	}
	tracked := rec.TrackedPoolRunners(901)
	if len(tracked) != 1 || tracked[0].ID != "c-busy" || !tracked[0].IsBusy {
		t.Fatalf("busy runner must remain tracked and busy, got %+v", tracked)
	}
}

// TestDrainPool_GracefulIsIdempotent verifies repeat graceful drains (RPC and
// removed-pool fallback racing) are no-ops — the busy runner is neither
// terminated nor double-counted (docs/25 §4.2).
func TestDrainPool_GracefulIsIdempotent(t *testing.T) {
	ctrl, engine, rec := newDrainHarness(t)
	ctx := context.Background()

	rec.TrackRunner(drainRunner("c-busy", "runnero-drain-busy", 902, true, time.Now().UTC().Add(-time.Hour)))

	ctrl.DrainPool(ctx, 902, "drain-pool", 0, true)
	ctrl.DrainPool(ctx, 902, "drain-pool", 0, true)

	if len(engine.TerminatedIDs) != 0 {
		t.Fatalf("repeat graceful drains must not terminate the busy runner, got %v", engine.TerminatedIDs)
	}
	if got := rec.TrackedPoolRunners(902); len(got) != 1 {
		t.Fatalf("busy runner must stay tracked, got %+v", got)
	}
}

// TestDrainPool_HardSupersedesGraceful verifies an explicit hard drain after
// a graceful one kills the spared busy runner and retires the backstop
// (docs/25 §4.2).
func TestDrainPool_HardSupersedesGraceful(t *testing.T) {
	ctrl, engine, rec := newDrainHarness(t)
	ctx := context.Background()

	rec.TrackRunner(drainRunner("c-busy", "runnero-drain-busy", 903, true, time.Now().UTC().Add(-time.Hour)))

	ctrl.DrainPool(ctx, 903, "drain-pool", 0, true)
	ctrl.DrainPool(ctx, 903, "drain-pool", 0, false)

	if len(engine.TerminatedIDs) != 1 || engine.TerminatedIDs[0] != "c-busy" {
		t.Fatalf("hard drain must terminate the spared busy runner, got %v", engine.TerminatedIDs)
	}

	// The backstop must not linger: CheckHungRunners stays a no-op.
	if err := ctrl.CheckHungRunners(ctx); err != nil {
		t.Fatalf("CheckHungRunners failed: %v", err)
	}
	if len(engine.TerminatedIDs) != 1 {
		t.Fatalf("no backstop kill expected after hard drain, got %v", engine.TerminatedIDs)
	}
}

// TestDrainBackstop_DefaultCapFires verifies docs/25 §4.4 + §8.3: a drained
// busy runner whose pool set no lifetime is force-terminated after
// DefaultDrainBackstop (6h) and the draining entry retires.
func TestDrainBackstop_DefaultCapFires(t *testing.T) {
	ctrl, engine, rec := newDrainHarness(t)
	ctx := context.Background()

	rec.TrackRunner(drainRunner("c-busy", "runnero-drain-busy", 904, true,
		time.Now().UTC().Add(-7*time.Hour))) // past the 6h default cap

	ctrl.DrainPool(ctx, 904, "drain-pool", 0, true)

	if err := ctrl.CheckHungRunners(ctx); err != nil {
		t.Fatalf("CheckHungRunners failed: %v", err)
	}
	if len(engine.TerminatedIDs) != 1 || engine.TerminatedIDs[0] != "c-busy" {
		t.Fatalf("backstop must force-terminate the drained hung runner, got %v", engine.TerminatedIDs)
	}
	if got := rec.TrackedPoolRunners(904); len(got) != 0 {
		t.Fatalf("killed runner must be untracked, got %+v", got)
	}

	// Entry retired: a further cycle neither crashes nor kills again.
	if err := ctrl.CheckHungRunners(ctx); err != nil {
		t.Fatalf("second CheckHungRunners failed: %v", err)
	}
	if len(engine.TerminatedIDs) != 1 {
		t.Fatalf("retired entry must not re-kill, got %v", engine.TerminatedIDs)
	}
}

// TestDrainBackstop_RespectsDeletedPoolLifetime verifies docs/25 §4.4: the
// deleted pool's own max_runner_lifetime wins over the default cap while it
// is longer.
func TestDrainBackstop_RespectsDeletedPoolLifetime(t *testing.T) {
	ctrl, engine, rec := newDrainHarness(t)
	ctx := context.Background()

	rec.TrackRunner(drainRunner("c-busy", "runnero-drain-busy", 905, true,
		time.Now().UTC().Add(-7*time.Hour)))

	// Pool had an 8h lifetime: 7h elapsed must survive the 6h default.
	ctrl.DrainPool(ctx, 905, "drain-pool", 8*time.Hour, true)
	if err := ctrl.CheckHungRunners(ctx); err != nil {
		t.Fatalf("CheckHungRunners failed: %v", err)
	}
	if len(engine.TerminatedIDs) != 0 {
		t.Fatalf("runner within the pool's own lifetime must survive, got %v", engine.TerminatedIDs)
	}

	// Past 8h (simulated by re-anchoring the clock), the backstop fires.
	stale := rec.TrackedPoolRunners(905)
	if len(stale) != 1 {
		t.Fatalf("runner must still be tracked, got %+v", stale)
	}
	rec.UntrackRunner(905, "c-busy")
	rec.TrackRunner(drainRunner("c-busy", "runnero-drain-busy", 905, true,
		time.Now().UTC().Add(-9*time.Hour)))

	if err := ctrl.CheckHungRunners(ctx); err != nil {
		t.Fatalf("second CheckHungRunners failed: %v", err)
	}
	if len(engine.TerminatedIDs) != 1 {
		t.Fatalf("backstop must fire past the pool's own lifetime, got %v", engine.TerminatedIDs)
	}
}

// TestDrainBackstop_CompletesWhenLastRunnerLeaves verifies entry retirement
// when the drained runner exits on its own (the normal path): the container
// "disappears" from tracking, and CheckHungRunners cleans up without kills
// (docs/25 §4.2).
func TestDrainBackstop_CompletesWhenLastRunnerLeaves(t *testing.T) {
	ctrl, engine, rec := newDrainHarness(t)
	ctx := context.Background()

	rec.TrackRunner(drainRunner("c-busy", "runnero-drain-busy", 906, true,
		time.Now().UTC().Add(-time.Hour)))
	ctrl.DrainPool(ctx, 906, "drain-pool", 0, true)

	// Job finished, ephemeral runner exited, audit untracked it.
	rec.UntrackRunner(906, "c-busy")

	if err := ctrl.CheckHungRunners(ctx); err != nil {
		t.Fatalf("CheckHungRunners failed: %v", err)
	}
	if len(engine.TerminatedIDs) != 0 {
		t.Fatalf("no backstop kill expected after self-exit, got %v", engine.TerminatedIDs)
	}

	// A new busy runner appearing under the retired pool id must NOT be
	// backstopped — the drain is complete, the entry is gone (fresh jobs
	// belong to a re-created pool with its own id).
	rec.TrackRunner(drainRunner("c-new", "runnero-drain-new", 906, true,
		time.Now().UTC().Add(-time.Hour)))
	if err := ctrl.CheckHungRunners(ctx); err != nil {
		t.Fatalf("second CheckHungRunners failed: %v", err)
	}
	if len(engine.TerminatedIDs) != 0 {
		t.Fatalf("retired pool must not backstop new runners, got %v", engine.TerminatedIDs)
	}
}

// TestRemovedPoolFallbackDrainsGracefully verifies docs/25 §4.5: the
// reconciler's removed-pool detection (restart adoption, out-of-band deletes)
// spares busy runners instead of killing them — the pre-RUN-127 hard behavior
// is gone from the fallback path.
func TestRemovedPoolFallbackDrainsGracefully(t *testing.T) {
	// Pool 907 was deleted in this database while the supervisor was down:
	// the tombstone is the ownership evidence that lets the fallback drain
	// its leftovers (RUN-239, docs/33 §3.2, row 2 of the matrix).
	repo := &mockPoolRepo{tombstones: map[int64]bool{907: true}}
	ctrl, engine, rec := newDrainHarnessWithRepo(t, repo)
	ctx := context.Background()

	now := time.Now().UTC()
	// The pool row is gone from the DB (deleted while the supervisor was
	// down), but the tombstone remains. The listing reports both runners as
	// live so boot adoption and the first audit keep them tracked
	// (RebuildState replaces state with the host listing).
	idle := drainRunner("c-idle", "runnero-fallback-idle", 907, false, time.Time{})
	busy := drainRunner("c-busy", "runnero-fallback-busy", 907, true, now.Add(-time.Hour))
	engine.AuditRunnersFn = func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
		return []orchestrator.RunnerStatus{idle, busy}, nil
	}

	if err := ctrl.Boot(ctx); err != nil {
		t.Fatalf("boot failed: %v", err)
	}

	if err := ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	if len(engine.TerminatedIDs) != 1 || engine.TerminatedIDs[0] != "c-idle" {
		t.Fatalf("fallback must terminate only idle runners, got %v", engine.TerminatedIDs)
	}
	tracked := rec.TrackedPoolRunners(907)
	if len(tracked) != 1 || tracked[0].ID != "c-busy" {
		t.Fatalf("fallback must spare the busy runner, got %+v", tracked)
	}

	// And the spared runner is backstopped like any graceful drain: replace
	// it with a 7h-old anchor (past the 6h default cap) and re-check. No
	// audit runs here, so the manual re-seed survives.
	rec.UntrackRunner(907, "c-busy")
	rec.TrackRunner(drainRunner("c-busy", "runnero-fallback-busy", 907, true,
		time.Now().UTC().Add(-7*time.Hour)))
	if err := ctrl.CheckHungRunners(ctx); err != nil {
		t.Fatalf("CheckHungRunners failed: %v", err)
	}
	if len(engine.TerminatedIDs) != 2 {
		t.Fatalf("fallback-drained runner must be backstopped at the default cap, got %v", engine.TerminatedIDs)
	}
}

// TestRemovedPoolForeignSkipped is the RUN-233 incident, encoded (docs/33
// §3.2, row 3 of the matrix): a second supervisor on a shared engine adopts
// the primary's runners, but its database never knew the pool and holds no
// tombstone - so the reconcile must skip the drain entirely, leave both
// runners tracked and running, and surface the discovery via ForeignPools.
func TestRemovedPoolForeignSkipped(t *testing.T) {
	// nil DB and tombstone-less DB both mean "no ownership evidence".
	for _, tc := range []struct {
		name string
		repo *mockPoolRepo
	}{
		{"nil database", nil},
		{"database without tombstone", &mockPoolRepo{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Pass a true nil interface for the nil-database case: a typed
			// (*mockPoolRepo)(nil) would panic inside the mock, not skip.
			var repo orchestrator.PoolRepository
			if tc.repo != nil {
				repo = tc.repo
			}
			ctrl, engine, rec := newDrainHarnessWithRepo(t, repo)
			ctx := context.Background()

			idle := drainRunner("c-idle", "runnero-foreign-idle", 907, false, time.Time{})
			busy := drainRunner("c-busy", "runnero-foreign-busy", 907, true, time.Now().UTC().Add(-time.Hour))
			engine.AuditRunnersFn = func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
				return []orchestrator.RunnerStatus{idle, busy}, nil
			}

			if err := ctrl.Boot(ctx); err != nil {
				t.Fatalf("boot failed: %v", err)
			}
			if err := ctrl.Reconcile(ctx); err != nil {
				t.Fatalf("reconcile failed: %v", err)
			}

			// The incident behavior - draining the foreign runners - must not happen.
			if len(engine.TerminatedIDs) != 0 {
				t.Fatalf("foreign runners must never be terminated, got %v", engine.TerminatedIDs)
			}
			if tracked := rec.TrackedPoolRunners(907); len(tracked) != 2 {
				t.Errorf("foreign runners must stay tracked, got %d", len(tracked))
			}
			if foreign := ctrl.ForeignPools(); len(foreign) != 1 || foreign[0].PoolID != 907 {
				t.Errorf("foreign pool must be surfaced, got %+v", foreign)
			}

			// Repeated cycles stay skipped and quiet: same outcome, no state churn.
			if err := ctrl.Reconcile(ctx); err != nil {
				t.Fatalf("second reconcile failed: %v", err)
			}
			if len(engine.TerminatedIDs) != 0 {
				t.Fatalf("second reconcile must not terminate foreign runners, got %v", engine.TerminatedIDs)
			}
		})
	}
}

// TestRemovedPoolTombstoneLookupFailsSafe verifies the fail-safe direction
// (docs/33 §3.2): when the ownership check errors out, the drain is skipped
// and the pool is recorded as foreign - uncertainty never destroys runners.
func TestRemovedPoolTombstoneLookupFailsSafe(t *testing.T) {
	repo := &mockPoolRepo{terr: errors.New("tombstone lookup unavailable")}
	ctrl, engine, rec := newDrainHarnessWithRepo(t, repo)
	ctx := context.Background()

	idle := drainRunner("c-idle", "runnero-errcheck-idle", 907, false, time.Time{})
	engine.AuditRunnersFn = func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
		return []orchestrator.RunnerStatus{idle}, nil
	}

	if err := ctrl.Boot(ctx); err != nil {
		t.Fatalf("boot failed: %v", err)
	}
	if err := ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	if len(engine.TerminatedIDs) != 0 {
		t.Fatalf("lookup failure must skip the drain, got %v", engine.TerminatedIDs)
	}
	if tracked := rec.TrackedPoolRunners(907); len(tracked) != 1 {
		t.Errorf("runner must stay tracked on lookup failure, got %d", len(tracked))
	}
	if foreign := ctrl.ForeignPools(); len(foreign) != 1 || foreign[0].PoolID != 907 {
		t.Errorf("pool must be recorded as foreign on lookup failure, got %+v", foreign)
	}
}

// TestRemovedPoolAdoptAllDrains verifies the RUN-241 escape hatch (docs/33
// §3.5): with EngineOwnership=adopt-all the tombstone gate is bypassed and
// the pre-docs/33 removed-pool drain runs even with zero ownership
// evidence (nil DB) - one-shot orphan takeover after a wiped data dir or
// engine move. strict (every test above) must keep skipping.
func TestRemovedPoolAdoptAllDrains(t *testing.T) {
	ctrl, engine, rec := newDrainHarnessWithMode(t, nil, "adopt-all")
	ctx := context.Background()

	idle := drainRunner("c-idle", "runnero-orphan-idle", 907, false, time.Time{})
	engine.AuditRunnersFn = func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
		return []orchestrator.RunnerStatus{idle}, nil
	}

	if err := ctrl.Boot(ctx); err != nil {
		t.Fatalf("boot failed: %v", err)
	}
	if err := ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	if len(engine.TerminatedIDs) != 1 || engine.TerminatedIDs[0] != "c-idle" {
		t.Fatalf("adopt-all must drain the orphan pool, got %v", engine.TerminatedIDs)
	}
	if foreign := ctrl.ForeignPools(); len(foreign) != 0 {
		t.Errorf("adopt-all must not record pools as foreign, got %+v", foreign)
	}
	if tracked := rec.TrackedPoolRunners(907); len(tracked) != 0 {
		t.Errorf("drained runner must be untracked, got %d", len(tracked))
	}
}

// newDrainHarnessWithMode wires the controller with an explicit engine
// ownership mode (RUN-241, docs/33 §3.5).
func newDrainHarnessWithMode(t *testing.T, repo orchestrator.PoolRepository, mode string) (*orchestrator.PoolController, *orchestrator.MockContainerProvider, *orchestrator.Reconciler) {
	t.Helper()
	engine := orchestrator.NewMockContainerProvider()
	reconciler := orchestrator.NewReconciler(engine)
	ctrl := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		ContainerEngine: engine,
		Reconciler:      reconciler,
		DB:              repo,
		EngineOwnership: mode,
	})
	return ctrl, engine, reconciler
}
