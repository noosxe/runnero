package orchestrator

import (
	"sync"
	"time"
)

// webhookDemandTTL bounds how long a queued job stays booked as demand
// without a terminal webhook delivery (missed in_progress/completed events).
// After the TTL the entry stops counting, so the next queued event converges
// back toward provisioning a fresh runner instead of waiting on a job the
// forge may never start.
const webhookDemandTTL = time.Hour

// webhookDemandTracker books queued-but-not-yet-started workflow jobs per
// pool so the queued-webhook spawn path can serve demand from warm idle
// runners before provisioning new ones (docs/03 §4). Without cumulative
// accounting, a burst of queued events would each observe the same warm
// runner and each skip its spawn, leaving the second and later jobs with no
// runner at all; the tracker lets each event see the demand already booked by
// its predecessors and spawn only the shortfall.
//
// In-memory only: a supervisor restart loses the accounting, which at worst
// reverts new events to spawn-per-event until the forge's own assignment
// catches up. Entries clear on the job's in_progress/completed delivery or
// via the TTL sweep.
type webhookDemandTracker struct {
	mu     sync.Mutex
	queued map[int64]map[int64]time.Time // poolID -> workflow job id -> last queuedAt
}

// markQueued books (or refreshes) a job's queued demand for the pool.
func (t *webhookDemandTracker) markQueued(poolID, jobID int64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.queued == nil {
		t.queued = make(map[int64]map[int64]time.Time)
	}
	poolSet, ok := t.queued[poolID]
	if !ok {
		poolSet = make(map[int64]time.Time)
		t.queued[poolID] = poolSet
	}
	// Refresh unconditionally: duplicate deliveries keep the entry alive, and
	// a re-queued job (workflow re-run reuses the job id) must not inherit a
	// timestamp old enough to be swept immediately.
	poolSet[jobID] = time.Now().UTC()
	t.sweepLocked(poolID)
}

// markStarted releases a job's demand booking when the forge reports it
// in_progress: its runner is busy, so the job no longer needs capacity.
func (t *webhookDemandTracker) markStarted(poolID, jobID int64) {
	t.remove(poolID, jobID)
}

// markFinished releases a job's demand booking when the forge reports it
// completed (including jobs cancelled while still queued: no runner was ever
// consumed).
func (t *webhookDemandTracker) markFinished(poolID, jobID int64) {
	t.remove(poolID, jobID)
}

// pending returns how many queued-but-unstarted jobs are booked for the pool.
func (t *webhookDemandTracker) pending(poolID int64) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.queued[poolID])
}

func (t *webhookDemandTracker) remove(poolID, jobID int64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if poolSet, ok := t.queued[poolID]; ok {
		delete(poolSet, jobID)
		if len(poolSet) == 0 {
			delete(t.queued, poolID)
		}
	}
}

func (t *webhookDemandTracker) sweepLocked(poolID int64) {
	cutoff := time.Now().UTC().Add(-webhookDemandTTL)
	for id, at := range t.queued[poolID] {
		if at.Before(cutoff) {
			delete(t.queued[poolID], id)
		}
	}
}
