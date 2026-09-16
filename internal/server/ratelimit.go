package server

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/noosxe/runnero/internal/db"
)

// Rate limit knobs (docs/32 section 4.2): a sliding 15-minute failure
// window per username+IP; the 5th failure within the window locks the key
// out with exponential backoff (1, 2, 4... minutes) capped at 15 minutes.
const (
	rateWindow       = 15 * time.Minute
	rateFailureLimit = 5
	rateBackoffBase  = time.Minute
	rateBackoffCap   = 15 * time.Minute
)

// RateLimitStore is the durable half of the login rate limiter (RUN-238,
// docs/32 section 4.2): failed logins are written through so a restart
// cannot reset an attacker's backoff, and in-window rows are reloaded on
// boot. *db.DB satisfies this interface via the sqlc-generated queries.
type RateLimitStore interface {
	InsertLoginRateFailure(ctx context.Context, arg db.InsertLoginRateFailureParams) error
	DeleteLoginRateFailuresByKey(ctx context.Context, rateKey string) error
	DeleteLoginRateFailuresBefore(ctx context.Context, cutoff time.Time) error
	ListLoginRateFailuresSince(ctx context.Context, failedAt time.Time) ([]db.ListLoginRateFailuresSinceRow, error)
}

// loginRateLimiter is the brute-force guard for the login path (docs/32
// section 4.2). Sliding window and backoff live in memory; with a non-nil
// store every failure is also persisted write-through (best-effort - the
// in-memory guard never depends on the database) and restore() reloads
// in-window rows at boot. The client IP feeds the key only - it never
// gates authorization.
type loginRateLimiter struct {
	mu       sync.Mutex
	failures map[string][]time.Time
	now      func() time.Time
	store    RateLimitStore
}

func newLoginRateLimiter() *loginRateLimiter {
	return &loginRateLimiter{
		failures: make(map[string][]time.Time),
		now:      time.Now,
	}
}

// newDurableLoginRateLimiter builds the limiter with SQLite write-through.
// The store is called on the login failure/success paths only; a store
// error degrades to plain in-memory behavior (fail-open) and is logged.
func newDurableLoginRateLimiter(store RateLimitStore) *loginRateLimiter {
	return &loginRateLimiter{
		failures: make(map[string][]time.Time),
		now:      time.Now,
		store:    store,
	}
}

// restore reloads persisted failures inside the sliding window (RUN-238).
// Rows at or older than the cutoff are past the window - they cannot
// contribute to any count or backoff - and are pruned from the table.
// Called once at construction; errors degrade to an empty slate.
func (l *loginRateLimiter) restore(now time.Time) {
	if l.store == nil {
		return
	}
	ctx := context.Background()
	cutoff := now.Add(-rateWindow)
	rows, err := l.store.ListLoginRateFailuresSince(ctx, cutoff)
	if err != nil {
		slog.Warn("login rate limiter: restore failed; starting empty", "err", err)
		return
	}
	l.mu.Lock()
	for _, row := range rows {
		l.failures[row.RateKey] = append(l.failures[row.RateKey], row.FailedAt)
	}
	l.mu.Unlock()
	if err := l.store.DeleteLoginRateFailuresBefore(ctx, cutoff); err != nil {
		slog.Warn("login rate limiter: pruning stale rows failed", "err", err)
	}
}

// retryAfter reports how long the key stays locked and whether a login
// attempt is currently refused. A locked key unlocks when the backoff
// derived from its in-window failure count has elapsed; the window slides,
// so stale failures stop counting (and stop extending the backoff) once
// they age out.
func (l *loginRateLimiter) retryAfter(key string) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.pruneLocked(key, now)

	fails := l.failures[key]
	if len(fails) < rateFailureLimit {
		return 0, false
	}

	backoff := rateBackoffBase << (len(fails) - rateFailureLimit)
	if backoff > rateBackoffCap || backoff <= 0 {
		backoff = rateBackoffCap
	}
	until := fails[len(fails)-1].Add(backoff)
	if !now.Before(until) {
		return 0, false
	}
	return until.Sub(now), true
}

// recordFailure appends a failure timestamp. Failures recorded while the
// key is locked still grow the backoff, so blind retries cannot outwait
// the guard. With a store configured, the failure is persisted
// write-through (best-effort) and out-of-window rows are pruned so the
// table stays bounded.
func (l *loginRateLimiter) recordFailure(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.pruneLocked(key, now)
	l.failures[key] = append(l.failures[key], now)

	if l.store == nil {
		return
	}
	ctx := context.Background()
	if err := l.store.InsertLoginRateFailure(ctx, db.InsertLoginRateFailureParams{
		RateKey:  key,
		FailedAt: now,
	}); err != nil {
		slog.Warn("login rate limiter: persisting failure failed", "err", err)
	}
	if err := l.store.DeleteLoginRateFailuresBefore(ctx, now.Add(-rateWindow)); err != nil {
		slog.Warn("login rate limiter: pruning stale rows failed", "err", err)
	}
}

// reset clears one key after a successful login (docs/32 section 4.2).
// Only the exact username+IP key resets: a successful login from address B
// must not release a lock held by an attacker at address A.
func (l *loginRateLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, key)

	if l.store == nil {
		return
	}
	// A successful login must also clear the durable rows, or the next
	// restart would resurrect the lockout for a key that just succeeded.
	if err := l.store.DeleteLoginRateFailuresByKey(context.Background(), key); err != nil {
		slog.Warn("login rate limiter: clearing persisted failures failed", "err", err)
	}
}

// pruneLocked drops failure timestamps that slid out of the window.
// Callers hold the mutex.
func (l *loginRateLimiter) pruneLocked(key string, now time.Time) {
	fails := l.failures[key]
	kept := fails[:0]
	for _, ts := range fails {
		if now.Sub(ts) < rateWindow {
			kept = append(kept, ts)
		}
	}
	if len(kept) == 0 {
		delete(l.failures, key)
		return
	}
	l.failures[key] = kept
}

// rateLimitKey derives the limiter key: username plus client IP
// (docs/32 section 4.2).
func rateLimitKey(username, clientIP string) string {
	return username + "\x00" + clientIP
}
