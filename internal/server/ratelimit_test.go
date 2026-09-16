package server

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/noosxe/runnero/internal/db"
	"time"
)

// Unit tests for the login brute-force guard (RUN-231, docs/32 §4.2):
// sliding window, threshold, exponential backoff with cap, reset on
// success, and key isolation.

func newTestLimiter() *loginRateLimiter {
	l := newLoginRateLimiter()
	now := time.Now()
	l.now = func() time.Time { return now }
	return l
}

func TestRateLimiterBelowThresholdNeverLocks(t *testing.T) {
	l := newTestLimiter()
	key := rateLimitKey("admin", "10.0.0.1")

	for i := 0; i < rateFailureLimit-1; i++ {
		l.recordFailure(key)
		if _, locked := l.retryAfter(key); locked {
			t.Fatalf("locked after %d failures, want unlocked until %d", i+1, rateFailureLimit)
		}
	}
}

func TestRateLimiterLocksAtThresholdWithExponentialBackoff(t *testing.T) {
	l := newTestLimiter()
	key := rateLimitKey("admin", "10.0.0.1")

	// Failures 1..4: unlocked. Failure 5 locks for 1 minute.
	for i := 0; i < rateFailureLimit-1; i++ {
		l.recordFailure(key)
	}
	l.recordFailure(key)

	retry, locked := l.retryAfter(key)
	if !locked {
		t.Fatal("not locked at threshold")
	}
	if retry <= 55*time.Second || retry > time.Minute {
		t.Fatalf("first lockout retry = %s, want ~1m", retry)
	}

	// Each further failure doubles the backoff: 2m, 4m, 8m, then cap 15m.
	want := []time.Duration{2 * time.Minute, 4 * time.Minute, 8 * time.Minute, rateBackoffCap, rateBackoffCap}
	for i, w := range want {
		l.recordFailure(key)
		retry, locked := l.retryAfter(key)
		if !locked {
			t.Fatalf("failure %d: want locked", rateFailureLimit+1+i)
		}
		if retry < w-time.Minute || retry > w {
			t.Fatalf("failure %d: retry = %s, want ~%s", rateFailureLimit+1+i, retry, w)
		}
	}
}

func TestRateLimiterWindowSlides(t *testing.T) {
	l := newTestLimiter()
	key := rateLimitKey("admin", "10.0.0.1")

	for i := 0; i < rateFailureLimit; i++ {
		l.recordFailure(key)
	}
	if _, locked := l.retryAfter(key); !locked {
		t.Fatal("want locked at threshold")
	}

	// Advance past window + backoff: all failures age out, lock releases.
	l.now = func() time.Time { return time.Now().Add(rateWindow + rateBackoffCap) }
	if _, locked := l.retryAfter(key); locked {
		t.Fatal("want unlocked after window slid past all failures")
	}
}

func TestRateLimiterSuccessResetsOnlyExactKey(t *testing.T) {
	l := newTestLimiter()
	victim := rateLimitKey("admin", "10.0.0.1")
	attacker := rateLimitKey("admin", "203.0.113.9")
	otherUser := rateLimitKey("root", "10.0.0.1")

	for i := 0; i < rateFailureLimit; i++ {
		l.recordFailure(victim)
		l.recordFailure(attacker)
		l.recordFailure(otherUser)
	}

	// A successful login from the victim's own IP resets exactly that key.
	l.reset(victim)
	if _, locked := l.retryAfter(victim); locked {
		t.Fatal("victim key still locked after successful login")
	}
	for name, key := range map[string]string{"attacker": attacker, "otherUser": otherUser} {
		if _, locked := l.retryAfter(key); !locked {
			t.Fatalf("%s key unlocked after an unrelated success", name)
		}
	}
}

func TestRateLimiterKeysAreIsolated(t *testing.T) {
	l := newTestLimiter()
	a := rateLimitKey("admin", "10.0.0.1")
	b := rateLimitKey("admin", "10.0.0.2")

	for i := 0; i < rateFailureLimit; i++ {
		l.recordFailure(a)
	}
	if _, locked := l.retryAfter(b); locked {
		t.Fatal("second IP locked by first IP's failures")
	}
}

// fakeRateLimitStore records the write-through calls the durable limiter
// makes, and replays persisted rows for boot restore (RUN-238).
type fakeRateLimitStore struct {
	mu        sync.Mutex
	rows      map[string][]time.Time
	inserts   int
	deletes   int
	prunes    int
	insertErr error
}

func newFakeRateLimitStore() *fakeRateLimitStore {
	return &fakeRateLimitStore{rows: make(map[string][]time.Time)}
}

func (f *fakeRateLimitStore) InsertLoginRateFailure(_ context.Context, arg db.InsertLoginRateFailureParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inserts++
	if f.insertErr != nil {
		return f.insertErr
	}
	f.rows[arg.RateKey] = append(f.rows[arg.RateKey], arg.FailedAt)
	return nil
}

func (f *fakeRateLimitStore) DeleteLoginRateFailuresByKey(_ context.Context, rateKey string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes++
	delete(f.rows, rateKey)
	return nil
}

func (f *fakeRateLimitStore) DeleteLoginRateFailuresBefore(_ context.Context, cutoff time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prunes++
	for key, ts := range f.rows {
		kept := ts[:0]
		for _, t := range ts {
			if t.After(cutoff) {
				kept = append(kept, t)
			}
		}
		if len(kept) == 0 {
			delete(f.rows, key)
		} else {
			f.rows[key] = kept
		}
	}
	return nil
}

func (f *fakeRateLimitStore) ListLoginRateFailuresSince(_ context.Context, since time.Time) ([]db.ListLoginRateFailuresSinceRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var rows []db.ListLoginRateFailuresSinceRow
	for key, ts := range f.rows {
		for _, t := range ts {
			if t.After(since) {
				rows = append(rows, db.ListLoginRateFailuresSinceRow{RateKey: key, FailedAt: t})
			}
		}
	}
	return rows, nil
}

// durableLimiter wires a limiter to the fake store with a fixed clock.
func durableLimiter(store *fakeRateLimitStore, now time.Time) *loginRateLimiter {
	l := newDurableLoginRateLimiter(store)
	l.now = func() time.Time { return now }
	return l
}

func TestDurableLimiterPersistsFailuresForRestart(t *testing.T) {
	now := time.Now()
	store := newFakeRateLimitStore()
	l := durableLimiter(store, now)
	key := rateLimitKey("admin", "10.0.0.1")

	for i := 0; i < rateFailureLimit; i++ {
		l.recordFailure(key)
	}
	if store.inserts != rateFailureLimit {
		t.Fatalf("store inserts = %d, want %d", store.inserts, rateFailureLimit)
	}

	// A fresh process rebuilds from the persisted rows: the lockout and its
	// remaining backoff survive the restart.
	reborn := newDurableLoginRateLimiter(store)
	reborn.now = func() time.Time { return now.Add(time.Second) }
	reborn.restore(time.Now())
	if retry, locked := reborn.retryAfter(key); !locked {
		t.Fatal("lockout lost after restore, want locked")
	} else if retry > rateBackoffBase {
		t.Fatalf("retry = %v, want at most the 1-minute first backoff", retry)
	}
}

func TestDurableLimiterRestoreIgnoresOutOfWindowRows(t *testing.T) {
	now := time.Now()
	store := newFakeRateLimitStore()
	ctx := context.Background()
	key := rateLimitKey("admin", "10.0.0.2")

	// Four in-window failures plus one that slid out: below threshold, and
	// the stale row must not resurrect a lockout on boot.
	stale := now.Add(-2 * rateWindow)
	for i := 0; i < rateFailureLimit-1; i++ {
		if err := store.InsertLoginRateFailure(ctx, db.InsertLoginRateFailureParams{RateKey: key, FailedAt: now.Add(-time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.InsertLoginRateFailure(ctx, db.InsertLoginRateFailureParams{RateKey: key, FailedAt: stale}); err != nil {
		t.Fatal(err)
	}

	l := newDurableLoginRateLimiter(store)
	l.now = func() time.Time { return now.Add(time.Second) }
	l.restore(now)
	if _, locked := l.retryAfter(key); locked {
		t.Fatal("locked after restore, want unlocked below threshold")
	}
}

func TestDurableLimiterResetClearsPersistedRows(t *testing.T) {
	now := time.Now()
	store := newFakeRateLimitStore()
	l := durableLimiter(store, now)
	key := rateLimitKey("admin", "10.0.0.3")

	for i := 0; i < rateFailureLimit; i++ {
		l.recordFailure(key)
	}
	l.reset(key)

	if _, locked := l.retryAfter(key); locked {
		t.Fatal("locked after reset, want unlocked")
	}
	if len(store.rows[key]) != 0 {
		t.Fatalf("persisted rows for key = %d, want 0 after reset", len(store.rows[key]))
	}
}

func TestDurableLimiterStoreFailureFailsOpen(t *testing.T) {
	now := time.Now()
	store := newFakeRateLimitStore()
	store.insertErr = errors.New("disk on fire")
	l := durableLimiter(store, now)
	key := rateLimitKey("admin", "10.0.0.4")

	// A failed persistence write must not take down or skip the in-memory
	// guard: the lockout still applies for this process's lifetime.
	for i := 0; i < rateFailureLimit; i++ {
		l.recordFailure(key)
	}
	if _, locked := l.retryAfter(key); !locked {
		t.Fatal("unlocked despite 5 failures, want in-memory lockout")
	}
}
