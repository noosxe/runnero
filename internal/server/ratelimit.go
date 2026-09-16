package server

import (
	"sync"
	"time"
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

// loginRateLimiter is the in-memory brute-force guard for the login path
// (docs/32 section 4.2). Single-process deployment by design: counters
// reset on restart, while the audit history persists. The client IP feeds
// the key only - it never gates authorization.
type loginRateLimiter struct {
	mu       sync.Mutex
	failures map[string][]time.Time
	now      func() time.Time
}

func newLoginRateLimiter() *loginRateLimiter {
	return &loginRateLimiter{
		failures: make(map[string][]time.Time),
		now:      time.Now,
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
// the guard.
func (l *loginRateLimiter) recordFailure(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.pruneLocked(key, now)
	l.failures[key] = append(l.failures[key], now)
}

// reset clears one key after a successful login (docs/32 section 4.2).
// Only the exact username+IP key resets: a successful login from address B
// must not release a lock held by an attacker at address A.
func (l *loginRateLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, key)
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
