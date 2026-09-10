package orchestrator

import (
	"testing"
	"time"
)

func TestWebhookDemandTrackerLifecycle(t *testing.T) {
	var tracker webhookDemandTracker

	if got := tracker.pending(42); got != 0 {
		t.Fatalf("expected empty tracker, got %d", got)
	}

	tracker.markQueued(42, 1)
	tracker.markQueued(42, 2)
	if got := tracker.pending(42); got != 2 {
		t.Fatalf("expected 2 pending, got %d", got)
	}

	// Duplicate delivery of the same job must not double-book demand.
	tracker.markQueued(42, 1)
	if got := tracker.pending(42); got != 2 {
		t.Fatalf("duplicate booking changed pending: got %d", got)
	}

	// Demand is per pool.
	tracker.markQueued(43, 1)
	if got := tracker.pending(43); got != 1 {
		t.Fatalf("expected pool 43 to be isolated, got %d", got)
	}
	if got := tracker.pending(42); got != 2 {
		t.Fatalf("pool 43 booking leaked into pool 42: got %d", got)
	}

	// in_progress releases the booking (the runner is busy now).
	tracker.markStarted(42, 1)
	if got := tracker.pending(42); got != 1 {
		t.Fatalf("expected 1 pending after start, got %d", got)
	}

	// completed releases the remaining booking.
	tracker.markFinished(42, 2)
	if got := tracker.pending(42); got != 0 {
		t.Fatalf("expected 0 pending after finish, got %d", got)
	}
}

func TestWebhookDemandTrackerSweepsStaleEntries(t *testing.T) {
	var tracker webhookDemandTracker

	tracker.markQueued(7, 1)
	// Age the booking past the TTL directly (internal state).
	tracker.queued[7][1] = time.Now().UTC().Add(-2 * webhookDemandTTL)

	// A fresh booking on the same pool sweeps the stale one.
	tracker.markQueued(7, 2)
	if got := tracker.pending(7); got != 1 {
		t.Fatalf("expected stale entry to be swept, got %d pending", got)
	}
	if _, exists := tracker.queued[7][1]; exists {
		t.Fatalf("stale entry still present")
	}
}
