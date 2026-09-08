package orchestrator_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/noosxe/runnero/internal/orchestrator"
	"github.com/noosxe/runnero/internal/provider"
)

// ghostName derives the runnero-prefixed registration name the sweep keys on
// (docs/20 §4.1): "runnero-" + SlugifyPoolName(pool) + "-" + random hex.
func ghostName(poolName, suffix string) string {
	return fmt.Sprintf("runnero-%s-%s", orchestrator.SlugifyPoolName(poolName), suffix)
}

// sweepCycles drives exactly n reconcile cycles against the harness mock.
func sweepCycles(h *busySyncHarness, n int) {
	ctx := context.Background()
	for i := 0; i < n; i++ {
		if err := h.ctrl.Reconcile(ctx); err != nil {
			panic(fmt.Sprintf("reconcile cycle %d failed: %v", i+1, err))
		}
	}
}

// TestGhostSweep_SweepsAfterConsecutiveOfflineCycles verifies the core rule
// (docs/20 §4.1): an idle, offline, locally-untracked runnero-* registration
// is deregistered only after N consecutive offline cycles (default 3), and
// exactly once.
func TestGhostSweep_SweepsAfterConsecutiveOfflineCycles(t *testing.T) {
	pool := busySyncPool("ghost-basic", 0, 5) // scale-to-zero: nothing tracked
	h := newBusySyncHarness(t, pool)
	if err := h.ctrl.Boot(context.Background()); err != nil {
		t.Fatalf("boot failed: %v", err)
	}

	ghost := ghostName(pool.Name, "a1b2c3")
	h.mockProv.remoteRunners = []provider.RemoteRunnerStatus{
		{Name: ghost, Busy: false, Online: false},
	}

	sweepCycles(h, 2)
	if len(h.mockProv.deregistered) != 0 {
		t.Fatalf("ghost must not be swept before %d consecutive cycles, got %v",
			orchestrator.DefaultGhostSweepOfflineCycles, h.mockProv.deregistered)
	}

	sweepCycles(h, 1)
	if len(h.mockProv.deregistered) != 1 || h.mockProv.deregistered[0] != ghost {
		t.Fatalf("expected exactly the ghost swept on cycle %d, got %v",
			orchestrator.DefaultGhostSweepOfflineCycles, h.mockProv.deregistered)
	}

	// The counter drops after a successful sweep: no repeat deregistrations.

	// Sweep lands on the third cycle. Afterwards the forge no longer lists
	// the deregistered runner — model that by emptying the listing.
	h.mockProv.remoteRunners = nil
	sweepCycles(h, 3)
	if len(h.mockProv.deregistered) != 1 {
		t.Fatalf("ghost must be deregistered exactly once, got %v", h.mockProv.deregistered)
	}
}

// TestGhostSweep_TrackedBusyAndForeignNeverSwept verifies the three exemption
// rules (docs/20 §4.1): tracked runners (owned by drain logic + the M23
// offline guard), busy registrations (possibly mid-job), and foreign names
// (manual runners / other automation) are never deregistered.
func TestGhostSweep_TrackedBusyAndForeignNeverSwept(t *testing.T) {
	pool := busySyncPool("ghost-guards", 0, 5)
	h := newBusySyncHarness(t, pool)
	if err := h.ctrl.Boot(context.Background()); err != nil {
		t.Fatalf("boot failed: %v", err)
	}

	// Locally tracked runner mid-job (busy) whose registration shows offline
	// and idle at the forge — only the tracked exemption may protect it.
	h.injectRunner(pool, ghostName(pool.Name, "tracked0"), true)

	h.mockProv.remoteRunners = []provider.RemoteRunnerStatus{
		{Name: ghostName(pool.Name, "tracked0"), Busy: false, Online: false}, // tracked → exempt
		{Name: ghostName(pool.Name, "busy1"), Busy: true, Online: false},     // busy → exempt
		{Name: "manual-runner", Busy: false, Online: false},                  // foreign → exempt
	}

	sweepCycles(h, 6) // well past the default cycle threshold
	if len(h.mockProv.deregistered) != 0 {
		t.Fatalf("exempt registrations must never be swept, got %v", h.mockProv.deregistered)
	}
}

// TestGhostSweep_CounterResetsWhenRunnerReturnsOnline verifies the consecutive
// requirement (docs/20 §4.1): a cycle in which the runner is online resets its
// offline counter, so flapping registrations are not swept prematurely.
func TestGhostSweep_CounterResetsWhenRunnerReturnsOnline(t *testing.T) {
	pool := busySyncPool("ghost-flap", 0, 5)
	h := newBusySyncHarness(t, pool)
	if err := h.ctrl.Boot(context.Background()); err != nil {
		t.Fatalf("boot failed: %v", err)
	}

	ghost := ghostName(pool.Name, "f1ap99")
	offline := []provider.RemoteRunnerStatus{{Name: ghost, Busy: false, Online: false}}
	online := []provider.RemoteRunnerStatus{{Name: ghost, Busy: false, Online: true}}

	h.mockProv.remoteRunners = offline
	sweepCycles(h, 2) // two consecutive offline cycles
	h.mockProv.remoteRunners = online
	sweepCycles(h, 1) // counter reset
	h.mockProv.remoteRunners = offline
	sweepCycles(h, 2) // only two consecutive offline cycles since reset
	if len(h.mockProv.deregistered) != 0 {
		t.Fatalf("flapping ghost must not be swept yet, got %v", h.mockProv.deregistered)
	}

	sweepCycles(h, 1) // third consecutive offline cycle
	if len(h.mockProv.deregistered) != 1 || h.mockProv.deregistered[0] != ghost {
		t.Fatalf("expected sweep after 3 consecutive offline cycles post-reset, got %v", h.mockProv.deregistered)
	}
}

// TestGhostSweep_DeregistrationErrorRetriesNextCycle verifies fail-open retry
// semantics (docs/20 §4.3): a failed deregistration keeps the counter intact
// so the next cycle retries, and a succeeding cycle sweeps exactly once.
func TestGhostSweep_DeregistrationErrorRetriesNextCycle(t *testing.T) {
	pool := busySyncPool("ghost-retry", 0, 5)
	h := newBusySyncHarness(t, pool)
	if err := h.ctrl.Boot(context.Background()); err != nil {
		t.Fatalf("boot failed: %v", err)
	}

	ghost := ghostName(pool.Name, "dead11")
	h.mockProv.remoteRunners = []provider.RemoteRunnerStatus{
		{Name: ghost, Busy: false, Online: false},
	}
	h.mockProv.deregErr = errors.New("forge API unavailable")

	sweepCycles(h, 5)
	if len(h.mockProv.deregistered) != 0 {
		t.Fatalf("failing deregistrations must not record successes, got %v", h.mockProv.deregistered)
	}
	if h.mockProv.deregCalls == 0 {
		t.Fatal("sweep must keep attempting deregistration across cycles")
	}

	h.mockProv.deregErr = nil
	sweepCycles(h, 1)
	if len(h.mockProv.deregistered) != 1 || h.mockProv.deregistered[0] != ghost {
		t.Fatalf("expected retry to succeed once the API recovers, got %v", h.mockProv.deregistered)
	}
}

// TestGhostSweep_SingleListingSharedPerCycle verifies the zero-extra-API-calls
// property (docs/20 §2): the busy-state sync and the ghost sweep consume one
// listing fetch per reconcile cycle, not one each.
func TestGhostSweep_SingleListingSharedPerCycle(t *testing.T) {
	pool := busySyncPool("ghost-shared", 0, 5)
	h := newBusySyncHarness(t, pool)
	if err := h.ctrl.Boot(context.Background()); err != nil {
		t.Fatalf("boot failed: %v", err)
	}
	h.mockProv.listCalls = 0 // isolate per-cycle behaviour from Boot's initial convergence pass

	h.injectRunner(pool, ghostName(pool.Name, "share0"), true) // busy sync has work too; busy survives drain
	h.mockProv.remoteRunners = []provider.RemoteRunnerStatus{
		{Name: ghostName(pool.Name, "share0"), Busy: true, Online: true},
		{Name: ghostName(pool.Name, "ghost9"), Busy: false, Online: false},
	}

	const cycles = 3
	sweepCycles(h, cycles)

	if h.mockProv.listCalls != cycles {
		t.Fatalf("expected exactly %d list calls for %d cycles (shared listing), got %d",
			cycles, cycles, h.mockProv.listCalls)
	}
	if len(h.mockProv.deregistered) != 1 {
		t.Fatalf("ghost should have been swept once, got %v", h.mockProv.deregistered)
	}
}
