package cron

import (
	"sync"
	"time"
)

// Clock provides an abstraction over time for the cron scheduler,
// allowing tests to use virtual clocks to simulate cron schedules without sleeping.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
	// Until returns a channel that receives the current time once the
	// clock reaches or passes the absolute deadline t. Callers that compute
	// a deadline up front must use Until rather than After, so the arming
	// cannot be shifted by concurrent time movement (a virtual clock can
	// jump between reading Now() and arming a relative timer).
	Until(t time.Time) <-chan time.Time
}

// RealClock implements Clock using standard library time in UTC.
type RealClock struct{}

// Now returns current time in UTC.
func (RealClock) Now() time.Time {
	return time.Now().UTC()
}

// After returns a channel that receives the current time after duration d.
func (RealClock) After(d time.Duration) <-chan time.Time {
	return time.After(d)
}

// Until returns a channel that receives the current time once the wall
// clock reaches t. A deadline already in the past fires immediately.
func (RealClock) Until(t time.Time) <-chan time.Time {
	return time.After(time.Until(t))
}

type virtualTimer struct {
	fireAt time.Time
	ch     chan time.Time
}

// VirtualClock is a thread-safe mock clock for testing that simulates the passage of time.
type VirtualClock struct {
	mu        sync.Mutex
	now       time.Time
	timers    []*virtualTimer
	waiterSig chan struct{}
}

// NewVirtualClock creates a VirtualClock set to the given initial UTC time.
func NewVirtualClock(initial time.Time) *VirtualClock {
	return &VirtualClock{
		now:       initial.UTC(),
		waiterSig: make(chan struct{}),
	}
}

// Now returns the current virtual time in UTC.
func (vc *VirtualClock) Now() time.Time {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	return vc.now
}

// After returns a channel that fires when the virtual clock advances to or past now + d.
func (vc *VirtualClock) After(d time.Duration) <-chan time.Time {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	return vc.timerLocked(vc.now.Add(d))
}

// Until returns a channel that fires when the virtual clock advances to or
// past the absolute deadline t. Arming is anchored to t itself, so a
// concurrent Advance between the caller computing t and this call cannot
// shift the deadline into the virtual future.
func (vc *VirtualClock) Until(t time.Time) <-chan time.Time {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	return vc.timerLocked(t.UTC())
}

// timerLocked arms a timer for the given absolute deadline; a deadline at
// or before the current virtual time fires immediately. The caller must hold vc.mu.
func (vc *VirtualClock) timerLocked(deadline time.Time) <-chan time.Time {
	ch := make(chan time.Time, 1)
	if !deadline.After(vc.now) {
		ch <- vc.now
		return ch
	}

	vt := &virtualTimer{
		fireAt: deadline,
		ch:     ch,
	}
	vc.timers = append(vc.timers, vt)
	vc.signalWaiterLocked()
	return ch
}

// Advance moves virtual time forward by d and fires any timers whose deadline has passed.
func (vc *VirtualClock) Advance(d time.Duration) {
	vc.mu.Lock()
	vc.now = vc.now.Add(d)
	vc.triggerTimersLocked()
	vc.mu.Unlock()
}

// Set explicitly sets virtual time to t (in UTC) and fires any timers whose deadline has passed.
func (vc *VirtualClock) Set(t time.Time) {
	vc.mu.Lock()
	vc.now = t.UTC()
	vc.triggerTimersLocked()
	vc.mu.Unlock()
}

func (vc *VirtualClock) triggerTimersLocked() {
	var remaining []*virtualTimer
	for _, vt := range vc.timers {
		if !vt.fireAt.After(vc.now) {
			select {
			case vt.ch <- vc.now:
			default:
			}
		} else {
			remaining = append(remaining, vt)
		}
	}
	vc.timers = remaining
}

// WaitersCount returns the number of active pending timers waiting to fire.
func (vc *VirtualClock) WaitersCount() int {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	return len(vc.timers)
}

// signalWaiterLocked wakes all goroutines blocked in WaitForWaiter.
// The caller must hold vc.mu.
func (vc *VirtualClock) signalWaiterLocked() {
	close(vc.waiterSig)
	vc.waiterSig = make(chan struct{})
}

// WaitForWaiter blocks until at least one timer is pending on the clock.
// It is deterministic — there is no wall-clock deadline — so tests can wait
// for a consumer (e.g. the scheduler loop) to arm its timer without
// sleep-polling or timeout tolerances. If no timer is ever registered it
// blocks forever, surfacing a broken consumer as a test hang rather than
// as a flaky timeout.
func (vc *VirtualClock) WaitForWaiter() {
	vc.mu.Lock()
	for len(vc.timers) == 0 {
		sig := vc.waiterSig
		vc.mu.Unlock()
		<-sig
		vc.mu.Lock()
	}
	vc.mu.Unlock()
}
