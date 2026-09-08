package orchestrator

import (
	"fmt"
	"time"

	"github.com/noosxe/runnero/internal/db"
)

const (
	// DefaultPollIntervalSeconds matches the migration default for
	// runner_pools.poll_interval_seconds (docs/24 §5.8).
	DefaultPollIntervalSeconds = 30
	// pollJitterFraction widens each effective interval by ±20% so multi-target
	// pools polling the same API host do not synchronize in lockstep (docs/24 §5.4).
	pollJitterFraction = 0.2
	// maxPollBackoffMultiplier caps rate-limit backoff at 5× the configured
	// interval: each fully-failed poll adds one interval of extra waiting (docs/24 §5.10).
	maxPollBackoffMultiplier = 5
)

// pollInterval returns the pool's configured demand-poll cadence. Values outside
// the validated 15–3600 range (zero on rows created before migration 006, or by
// direct DB writes) mean "no throttle": the audit cycle's own cadence applies.
// This also keeps single-cycle test harnesses deterministic.
func pollInterval(p db.RunnerPool) time.Duration {
	if p.PollIntervalSeconds <= 0 {
		return 0
	}
	return time.Duration(p.PollIntervalSeconds) * time.Second
}

// pollDue reports whether the pool's demand-poll throttle has elapsed, applying
// the per-pool interval, ±20% jitter, and failure backoff (docs/24 §5.4, §5.10).
func (c *PoolController) pollDue(p db.RunnerPool) bool {
	interval := pollInterval(p)
	if interval <= 0 {
		return true
	}

	c.pollMu.Lock()
	defer c.pollMu.Unlock()

	last, ok := c.lastPollAt[p.ID]
	if !ok {
		return true
	}

	backoff := c.pollBackoff[p.ID]
	if backoff > maxPollBackoffMultiplier {
		backoff = maxPollBackoffMultiplier
	}

	jitter := 1.0
	if c.jitterFn != nil {
		jitter += (2*c.jitterFn() - 1) * pollJitterFraction
	}
	due := last.Add(time.Duration(float64(interval) * float64(1+backoff) * jitter))
	return !time.Now().UTC().Before(due)
}

// recordPollOutcome stores the poll completion time, updates the failure
// backoff (each fully-failed poll adds one interval, capped; any successful
// target resets it), and publishes the poll observation to the pool
// diagnostics (docs/24 §5.9). Partial failures keep the queued count of the
// targets that answered and log the rest (docs/24 §5.10).
func (c *PoolController) recordPollOutcome(poolName string, poolID int64, queued, failures, targets int, note string) {
	c.pollMu.Lock()
	c.lastPollAt[poolID] = time.Now().UTC()
	if targets > 0 && failures == targets {
		c.pollBackoff[poolID]++
		if c.pollBackoff[poolID] > maxPollBackoffMultiplier {
			c.pollBackoff[poolID] = maxPollBackoffMultiplier
		}
	} else if failures == 0 {
		c.pollBackoff[poolID] = 0
	}
	c.pollMu.Unlock()

	var pollErr string
	switch {
	case targets > 0 && failures == targets:
		pollErr = fmt.Sprintf("poll failed for all %d target(s)", targets)
	case note != "":
		pollErr = note
	}
	c.setPoolPollObservation(poolName, poolID, queued, pollErr)
}

// setPoolPollObservation records the latest demand-poll outcome on the pool
// diagnostics without touching health status or error fields: a failed poll is
// a degraded input signal, not a degraded pool (docs/24 §5.9).
func (c *PoolController) setPoolPollObservation(poolName string, poolID int64, queued int, pollErr string) {
	c.diagMu.Lock()
	defer c.diagMu.Unlock()

	diag, ok := c.diagnostics[poolID]
	if !ok {
		diag = PoolDiagnosticState{
			PoolID:   poolID,
			PoolName: poolName,
		}
	}
	diag.LastPollAt = time.Now().UTC()
	diag.LastPollQueuedCount = queued
	diag.LastPollError = pollErr
	c.diagnostics[poolID] = diag
}
