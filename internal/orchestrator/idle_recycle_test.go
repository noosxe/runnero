package orchestrator_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/noosxe/runnero/internal/db"
	"github.com/noosxe/runnero/internal/orchestrator"
)

// recycleFixture is a controller tracking idle runners of pool 501 without a
// booted control loop: Reconcile is a no-op, so each test observes the
// background recycle pass in isolation (docs/22 §6.2).
type recycleFixture struct {
	ctrl       *orchestrator.PoolController
	engine     *orchestrator.MockContainerProvider
	reconciler *orchestrator.Reconciler

	mu   sync.Mutex
	live []orchestrator.RunnerStatus
}

func idleRunner(id string) orchestrator.RunnerStatus {
	return orchestrator.RunnerStatus{PoolID: 501, ID: id, Name: "runnero-recycle-" + id, PoolName: "recycle-pool", State: "running"}
}

func newRecycleFixture(t *testing.T, timeout time.Duration, runners ...orchestrator.RunnerStatus) *recycleFixture {
	t.Helper()
	f := &recycleFixture{live: runners}
	f.engine = &orchestrator.MockContainerProvider{
		AuditRunnersFn: func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return append([]orchestrator.RunnerStatus(nil), f.live...), nil
		},
	}
	f.reconciler = orchestrator.NewReconciler(f.engine)
	repo := &mockPoolRepo{pools: []db.RunnerPool{{ID: 501, Name: "recycle-pool", Provider: "github", Scope: "repo"}}}
	f.ctrl = orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:                 repo,
		ContainerEngine:    f.engine,
		Reconciler:         f.reconciler,
		IdleRecycleTimeout: timeout,
	})
	f.adopt(t, runners...)
	return f
}

// adopt replaces the engine's live set and re-audits so the reconciler tracks it.
func (f *recycleFixture) adopt(t *testing.T, runners ...orchestrator.RunnerStatus) {
	t.Helper()
	f.mu.Lock()
	f.live = runners
	f.mu.Unlock()
	if _, err := f.reconciler.RebuildState(context.Background()); err != nil {
		t.Fatalf("RebuildState failed: %v", err)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func shutdownWithin(t *testing.T, ctrl *orchestrator.PoolController, limit time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	if err := ctrl.GracefulShutdown(ctx); err != nil {
		t.Fatalf("GracefulShutdown failed: %v", err)
	}
	if elapsed := time.Since(start); elapsed > limit {
		t.Fatalf("GracefulShutdown took %v, want < %v", elapsed, limit)
	}
}

// TestScheduleIdleRecycle_ReturnsBeforeRecycleCompletes pins the RPC-facing
// contract: scheduling returns while the recycle is still blocked in the
// container engine, the pass then completes in the background, and the pool
// diagnostics publish the recycle as progress for WatchPools.
func TestScheduleIdleRecycle_ReturnsBeforeRecycleCompletes(t *testing.T) {
	f := newRecycleFixture(t, 0, idleRunner("a"), idleRunner("b"))

	release := make(chan struct{})
	started := make(chan string, 4)
	var done atomic.Int32
	f.engine.TerminateRunnerFn = func(ctx context.Context, id string) error {
		started <- id
		<-release
		done.Add(1)
		return nil
	}

	returned := make(chan struct{})
	go func() {
		f.ctrl.ScheduleIdleRecycle(501)
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("ScheduleIdleRecycle blocked on the recycle")
	}

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("background recycle never reached the container engine")
	}
	if got := done.Load(); got != 0 {
		t.Fatalf("recycle completed %d terminations before release", got)
	}
	if intent := f.ctrl.PoolDiagnostics(501).CurrentIntent; !strings.Contains(intent, "Recycling 2 idle runner(s)") {
		t.Errorf("in-flight recycle must publish progress through the pool intent, got %q", intent)
	}

	close(release)
	waitFor(t, "both idle runners recycled", func() bool { return done.Load() == 2 })
	waitFor(t, "recycled runners untracked", func() bool {
		_, idle := f.ctrl.PoolStats(501)
		return idle == 0
	})
	shutdownWithin(t, f.ctrl, 2*time.Second)
}

// TestScheduleIdleRecycle_SerializesAndCoalescesEdits verifies back-to-back
// edits of one pool never run two recycle passes concurrently, and that edits
// landing mid-pass still get a follow-up pass that picks up runners spawned
// in between.
func TestScheduleIdleRecycle_SerializesAndCoalescesEdits(t *testing.T) {
	f := newRecycleFixture(t, 0, idleRunner("first"))

	release := make(chan struct{})
	started := make(chan string, 8)
	var inFlight, maxInFlight atomic.Int32
	var mu sync.Mutex
	var terminated []string
	f.engine.TerminateRunnerFn = func(ctx context.Context, id string) error {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			m := maxInFlight.Load()
			if n <= m || maxInFlight.CompareAndSwap(m, n) {
				break
			}
		}
		started <- id
		if id == "first" {
			<-release
		}
		mu.Lock()
		terminated = append(terminated, id)
		mu.Unlock()
		return nil
	}

	f.ctrl.ScheduleIdleRecycle(501)
	<-started

	// A runner spawned while the first pass is blocked, then two more edits.
	f.adopt(t, idleRunner("first"), idleRunner("second"))
	f.ctrl.ScheduleIdleRecycle(501)
	f.ctrl.ScheduleIdleRecycle(501)

	close(release)
	waitFor(t, "rerun pass recycles the runner spawned mid-pass", func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, id := range terminated {
			if id == "second" {
				return true
			}
		}
		return false
	})
	if got := maxInFlight.Load(); got != 1 {
		t.Errorf("recycle passes of one pool must be serialized, saw %d concurrent terminations", got)
	}
	shutdownWithin(t, f.ctrl, 2*time.Second)
}

// TestScheduleIdleRecycle_FailureIsContained verifies a failing termination
// is logged and does not wedge the per-pool worker: once the audit re-adopts
// the surviving container, a later edit schedules a fresh pass.
func TestScheduleIdleRecycle_FailureIsContained(t *testing.T) {
	f := newRecycleFixture(t, 0, idleRunner("broken"))

	var calls atomic.Int32
	f.engine.TerminateRunnerFn = func(ctx context.Context, id string) error {
		calls.Add(1)
		return errors.New("docker: removal failed")
	}

	f.ctrl.ScheduleIdleRecycle(501)
	waitFor(t, "first failing pass", func() bool { return calls.Load() == 1 })

	// The container survived the failed removal; the next audit re-adopts it
	// and the worker, which exited after the failing pass, runs again.
	f.adopt(t, idleRunner("broken"))
	waitFor(t, "a later edit runs a fresh pass", func() bool {
		f.ctrl.ScheduleIdleRecycle(501)
		return calls.Load() >= 2
	})
	f.engine.TerminateRunnerFn = nil
	shutdownWithin(t, f.ctrl, 2*time.Second)
}

// TestScheduleIdleRecycle_TimeoutBoundsPass verifies a recycle stuck in the
// container engine gives up after IdleRecycleTimeout instead of holding the
// provisioning lock forever.
func TestScheduleIdleRecycle_TimeoutBoundsPass(t *testing.T) {
	f := newRecycleFixture(t, 50*time.Millisecond, idleRunner("stuck"))

	var aborted atomic.Bool
	f.engine.TerminateRunnerFn = func(ctx context.Context, id string) error {
		<-ctx.Done()
		aborted.Store(true)
		return ctx.Err()
	}

	f.ctrl.ScheduleIdleRecycle(501)
	waitFor(t, "stuck pass aborted by its deadline", aborted.Load)

	// The provisioning lock is free again: a synchronous reconcile entry
	// point completes promptly.
	f.engine.TerminateRunnerFn = nil
	done := make(chan struct{})
	go func() {
		_ = f.ctrl.RecycleIdleRunners(context.Background(), 501)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed-out recycle pass still holds the provisioning lock")
	}
	shutdownWithin(t, f.ctrl, 2*time.Second)
}

// TestScheduleIdleRecycle_ShutdownDoesNotHang verifies a daemon shutdown
// mid-recycle cancels the pass instead of waiting out the recycle timeout,
// and that no recycle is scheduled once shutdown began.
func TestScheduleIdleRecycle_ShutdownDoesNotHang(t *testing.T) {
	f := newRecycleFixture(t, time.Hour, idleRunner("mid-shutdown"))

	started := make(chan struct{})
	var once sync.Once
	var calls atomic.Int32
	f.engine.TerminateRunnerFn = func(ctx context.Context, id string) error {
		calls.Add(1)
		first := false
		once.Do(func() { first = true; close(started) })
		if first {
			// Only the background pass blocks; shutdown's own teardown of
			// the still-tracked runner returns immediately.
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}

	f.ctrl.ScheduleIdleRecycle(501)
	<-started
	shutdownWithin(t, f.ctrl, 2*time.Second)

	before := calls.Load()
	f.adopt(t, idleRunner("after-shutdown"))
	f.ctrl.ScheduleIdleRecycle(501)
	time.Sleep(50 * time.Millisecond)
	if got := calls.Load(); got != before {
		t.Errorf("no recycle may run after shutdown, terminations %d -> %d", before, got)
	}
}
