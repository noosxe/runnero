package orchestrator

import (
	"testing"
	"time"

	"github.com/noosxe/runnero/internal/db"
)

// newPollThrottleController builds a bare controller with deterministic jitter
// (midpoint → effective multiplier exactly 1.0) for throttle/backoff tests.
func newPollThrottleController() *PoolController {
	return &PoolController{
		lastPollAt:  make(map[int64]time.Time),
		pollBackoff: make(map[int64]int),
		jitterFn:    func() float64 { return 0.5 },
		diagnostics: make(map[int64]PoolDiagnosticState),
	}
}

func TestPollDue_FirstPollAlwaysDue(t *testing.T) {
	c := newPollThrottleController()
	p := db.RunnerPool{ID: 1, PollIntervalSeconds: 30}
	if !c.pollDue(p) {
		t.Fatal("first poll must be due")
	}
}

func TestPollDue_ThrottledWithinInterval(t *testing.T) {
	c := newPollThrottleController()
	p := db.RunnerPool{ID: 1, PollIntervalSeconds: 30}

	c.recordPollOutcome("p", 1, 0, 0, 1, "")
	if c.pollDue(p) {
		t.Fatal("poll must be throttled within the configured interval")
	}
}

func TestPollDue_DueAfterIntervalElapsed(t *testing.T) {
	c := newPollThrottleController()
	p := db.RunnerPool{ID: 1, PollIntervalSeconds: 30}

	c.pollMu.Lock()
	c.lastPollAt[1] = time.Now().UTC().Add(-45 * time.Second)
	c.pollMu.Unlock()
	if !c.pollDue(p) {
		t.Fatal("poll must be due after the interval elapsed")
	}
}

func TestPollDue_BackoffExtendsWaitAndSuccessResets(t *testing.T) {
	c := newPollThrottleController()
	p := db.RunnerPool{ID: 1, PollIntervalSeconds: 30}

	// One fully-failed poll → backoff 2×: due only after ~60s (±jitter).
	c.recordPollOutcome("p", 1, 0, 1, 1, "")

	c.pollMu.Lock()
	c.lastPollAt[1] = time.Now().UTC().Add(-45 * time.Second)
	c.pollMu.Unlock()
	if c.pollDue(p) {
		t.Fatal("backoff must extend the wait beyond the plain interval")
	}

	c.pollMu.Lock()
	c.lastPollAt[1] = time.Now().UTC().Add(-70 * time.Second)
	c.pollMu.Unlock()
	if !c.pollDue(p) {
		t.Fatal("poll must be due after the backoff-extended interval")
	}

	// A fully-successful poll resets the backoff.
	c.recordPollOutcome("p", 1, 5, 0, 1, "")
	if c.pollBackoff[1] != 0 {
		t.Fatalf("success must reset backoff, got %d", c.pollBackoff[1])
	}

	// A partial failure (some targets answered) must not grow the backoff.
	c.recordPollOutcome("p", 1, 0, 1, 2, "")
	if c.pollBackoff[1] != 0 {
		t.Fatalf("partial failure must not grow backoff, got %d", c.pollBackoff[1])
	}
}

func TestPollDue_BackoffCapped(t *testing.T) {
	c := newPollThrottleController()
	p := db.RunnerPool{ID: 1, PollIntervalSeconds: 15}

	for i := 0; i < 20; i++ {
		c.recordPollOutcome("p", 1, 0, 1, 1, "")
	}
	if c.pollBackoff[1] > maxPollBackoffMultiplier {
		t.Fatalf("backoff must be capped at %d, got %d", maxPollBackoffMultiplier, c.pollBackoff[1])
	}
	_ = p
}

func TestPollDue_ZeroIntervalMeansNoThrottle(t *testing.T) {
	c := newPollThrottleController()
	legacy := db.RunnerPool{ID: 2, PollIntervalSeconds: 0}

	c.recordPollOutcome("p", 2, 0, 0, 1, "")
	if !c.pollDue(legacy) {
		t.Fatal("zero interval must mean no throttle (docs/24 §5.4)")
	}
}

func TestRecordPollOutcome_PublishesDiagnostics(t *testing.T) {
	c := newPollThrottleController()

	c.recordPollOutcome("p", 7, 3, 0, 1, "")
	diag := c.PoolDiagnostics(7)
	if diag.LastPollQueuedCount != 3 {
		t.Errorf("expected queued count 3, got %d", diag.LastPollQueuedCount)
	}
	if diag.LastPollError != "" {
		t.Errorf("expected clean poll error, got %q", diag.LastPollError)
	}
	if diag.LastPollAt.IsZero() {
		t.Error("expected LastPollAt to be recorded")
	}
	if diag.HealthStatus != "" {
		t.Errorf("poll observation must not set health status, got %q", diag.HealthStatus)
	}

	c.recordPollOutcome("p", 7, 0, 1, 1, "poll skipped: no org-level queued-jobs API")
	if diag = c.PoolDiagnostics(7); diag.LastPollError != "poll failed for all 1 target(s)" {
		t.Errorf("full failure must win over note, got %q", diag.LastPollError)
	}
}

func TestDefaultPollIntervalSeconds(t *testing.T) {
	if DefaultPollIntervalSeconds != 30 {
		t.Fatalf("default poll interval must stay 30s per docs/24 §7, got %d", DefaultPollIntervalSeconds)
	}
}
