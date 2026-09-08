package orchestrator_test

import (
	"context"
	"testing"
	"time"

	"github.com/noosxe/runnero/internal/orchestrator"
	"github.com/noosxe/runnero/internal/provider"
)

// TestLifetimeAnchor_AdoptionSeedsSpawnAnchor verifies docs/23 §4.4.1: a
// running container adopted by the reconciler (boot/restart) gets its busy
// anchor seeded with SpawnedAt — the host listing cannot report busy state, so
// the conservative seed keeps an already-mid-job runner at exactly the
// pre-docs/23 spawn+lifetime bound. A Docker-style re-listing that carries no
// BusySince must not clobber the known anchor (docs/23 §4.2 merge rule).
func TestLifetimeAnchor_AdoptionSeedsSpawnAnchor(t *testing.T) {
	spawned := time.Now().UTC().Add(-3 * time.Hour)
	hostListing := []orchestrator.RunnerStatus{
		{
			ID:        "c-adopted-1",
			Name:      "runnero-adopted-1",
			PoolName:  "adopt-pool",
			PoolID:    300,
			State:     "running",
			SpawnedAt: spawned,
		},
	}

	mockEngine := &orchestrator.MockContainerProvider{
		AuditRunnersFn: func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
			// Return a fresh copy each cycle, mirroring a real provider that
			// reconstructs status from container labels (BusySince always zero
			// in the listing — it is supervisor-ephemeral state).
			out := make([]orchestrator.RunnerStatus, len(hostListing))
			copy(out, hostListing)
			return out, nil
		},
		PingFn: func(ctx context.Context) error { return nil },
	}
	reconciler := orchestrator.NewReconciler(mockEngine)

	report, err := reconciler.Audit(context.Background())
	if err != nil {
		t.Fatalf("audit failed: %v", err)
	}
	if len(report.Adopted) != 1 {
		t.Fatalf("expected 1 adopted runner, got %+v", report.Adopted)
	}
	if !report.Adopted[0].BusySince.Equal(spawned) {
		t.Fatalf("adopted running container must get BusySince=SpawnedAt, got %v want %v",
			report.Adopted[0].BusySince, spawned)
	}

	// Busy flip after adoption: set-once keeps the seeded spawn anchor
	// (docs/23 §4.2) — the runner was likely mid-job before the restart.
	reconciler.MarkRunnerBusy("runnero-adopted-1", true)
	tracked := reconciler.TrackedPoolRunners(300)
	if len(tracked) != 1 || !tracked[0].IsBusy {
		t.Fatalf("expected adopted runner busy after flip, got %+v", tracked)
	}
	if !tracked[0].BusySince.Equal(spawned) {
		t.Fatalf("set-once anchor must keep the adoption seed, got %v want %v",
			tracked[0].BusySince, spawned)
	}

	// Re-audit with a BusySince-less listing: the merge must preserve the
	// known anchor exactly like SpawnedAt/IsBusy/OnDemand.
	if _, err := reconciler.Audit(context.Background()); err != nil {
		t.Fatalf("re-audit failed: %v", err)
	}
	tracked = reconciler.TrackedPoolRunners(300)
	if len(tracked) != 1 {
		t.Fatalf("expected 1 tracked runner after re-audit, got %+v", tracked)
	}
	if !tracked[0].BusySince.Equal(spawned) {
		t.Fatalf("re-audit must preserve the busy anchor, got %v want %v",
			tracked[0].BusySince, spawned)
	}
}

// TestLifetimeAnchor_StickyAcrossBusyFlaps verifies docs/23 §4.4: the anchor is
// set once at the first idle→busy transition and survives busy→idle→busy flaps
// (docs/19 §2.4 worst case: one audit cycle) — a listing race cannot extend a
// hung job's wall clock by resetting the clock.
func TestLifetimeAnchor_StickyAcrossBusyFlaps(t *testing.T) {
	mockEngine := &orchestrator.MockContainerProvider{
		PingFn: func(ctx context.Context) error { return nil },
	}
	reconciler := orchestrator.NewReconciler(mockEngine)
	spawned := time.Now().UTC().Add(-2 * time.Hour)
	reconciler.TrackRunner(orchestrator.RunnerStatus{
		ID:        "c-flap-1",
		Name:      "runnero-flap-1",
		PoolName:  "flap-pool",
		PoolID:    301,
		State:     "running",
		SpawnedAt: spawned,
	})

	// Webhook fast path (docs/23 §4.5): in_progress stamps the anchor.
	reconciler.MarkRunnerBusy("runnero-flap-1", true)
	anchor := trackedAnchor(t, reconciler, 301, "c-flap-1")
	if anchor.IsZero() {
		t.Fatal("idle→busy transition must stamp BusySince")
	}
	if !anchor.After(spawned) {
		t.Fatalf("anchor must reflect pickup, not spawn: got %v, spawned %v", anchor, spawned)
	}

	// Missed 'completed' webhook healed by a stale/idle listing, then busy
	// again: the earliest anchor wins (set-once, sticky).
	time.Sleep(5 * time.Millisecond) // ensure a later "now" would differ
	reconciler.MarkRunnerBusy("runnero-flap-1", false)
	reconciler.MarkRunnerBusy("runnero-flap-1", true)

	if got := trackedAnchor(t, reconciler, 301, "c-flap-1"); !got.Equal(anchor) {
		t.Fatalf("anchor must be sticky across busy flaps: got %v, want %v", got, anchor)
	}
	if r := trackedBy(t, reconciler, 301, "c-flap-1"); !r.IsBusy {
		t.Fatal("runner must be busy after re-flip")
	}
}

// TestLifetimeAnchor_BusySyncLateDetectionBoundsDeadline verifies docs/23
// §4.5: a runner whose pickup was only observed by the busy-state sync (missed
// webhook) gets its anchor at the observation time — up to one audit cycle
// after actual pickup. The lifetime deadline therefore skews by the
// observation delay and never fires early: a runner spawned far past the limit
// that just went busy must survive the check.
func TestLifetimeAnchor_BusySyncLateDetectionBoundsDeadline(t *testing.T) {
	ctx := context.Background()
	pool := busySyncPool("anchor-late-detect", 1, 5)
	pool.MaxRunnerLifetimeSeconds = 5 // 5s limit
	h := newBusySyncHarness(t, pool)

	if err := h.ctrl.Boot(ctx); err != nil {
		t.Fatalf("boot failed: %v", err)
	}

	// A long-lived idle standby: spawned 10 minutes ago, well past the 5s
	// limit. Under the old spawn-anchored semantics it would have been
	// force-terminated on the first cycle.
	status := orchestrator.RunnerStatus{
		ID:        "c-late-busy",
		Name:      "runnero-late-busy",
		PoolName:  pool.Name,
		PoolID:    pool.ID,
		State:     "running",
		IsBusy:    false,
		SpawnedAt: time.Now().UTC().Add(-10 * time.Minute),
	}
	h.liveMu.Lock()
	h.liveRunners[status.ID] = status
	h.liveMu.Unlock()
	h.reconciler.TrackRunner(status)

	// The forge now reports it busy (pickup happened without a webhook).
	h.mockProv.remoteRunners = []provider.RemoteRunnerStatus{
		{Name: "runnero-late-busy", Busy: true, Online: true},
	}

	// Cycle 1: hung check runs before busy-sync — not yet busy locally, so it
	// is skipped; busy-sync then flips it busy and stamps the anchor at the
	// observation time.
	if err := h.ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	anchor := trackedAnchor(t, h.reconciler, pool.ID, "c-late-busy")
	if anchor.IsZero() {
		t.Fatal("busy-sync observation must stamp BusySince")
	}
	if !anchor.After(status.SpawnedAt.Add(9 * time.Minute)) {
		t.Fatalf("anchor must be the observation time, not spawn: got %v", anchor)
	}

	// Cycle 2: busy with a fresh anchor — elapsed ≈ 0 < 5s limit, survives.
	if err := h.ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("second reconcile failed: %v", err)
	}

	if len(h.terminated) != 0 {
		t.Fatalf("recently observed busy runner must not be terminated, got %v", h.terminated)
	}
	r := trackedBy(t, h.reconciler, pool.ID, "c-late-busy")
	if !r.IsBusy {
		t.Fatal("runner must remain tracked and busy")
	}
}

func trackedAnchor(t *testing.T, r *orchestrator.Reconciler, poolID int64, id string) time.Time {
	t.Helper()
	return trackedBy(t, r, poolID, id).BusySince
}

func trackedBy(t *testing.T, r *orchestrator.Reconciler, poolID int64, id string) orchestrator.RunnerStatus {
	t.Helper()
	for _, s := range r.TrackedPoolRunners(poolID) {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("runner %q not tracked in pool %d", id, poolID)
	return orchestrator.RunnerStatus{}
}

