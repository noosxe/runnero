package cron

import (
	"testing"
	"time"
)

func TestRealClock(t *testing.T) {
	clk := RealClock{}
	now := clk.Now()
	if now.Location() != time.UTC {
		t.Errorf("expected UTC location, got %v", now.Location())
	}

	ch := clk.After(10 * time.Millisecond)
	select {
	case fired := <-ch:
		if fired.IsZero() {
			t.Error("expected non-zero time from After channel")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("RealClock.After timed out")
	}
}

func TestVirtualClock_AdvanceAndSet(t *testing.T) {
	initial := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	vc := NewVirtualClock(initial)

	if !vc.Now().Equal(initial) {
		t.Fatalf("expected %v, got %v", initial, vc.Now())
	}

	ch1 := vc.After(1 * time.Hour)
	ch2 := vc.After(2 * time.Hour)

	if vc.WaitersCount() != 2 {
		t.Fatalf("expected 2 waiters, got %d", vc.WaitersCount())
	}

	// Advance 30 mins: neither should fire
	vc.Advance(30 * time.Minute)
	select {
	case <-ch1:
		t.Fatal("ch1 fired prematurely")
	case <-ch2:
		t.Fatal("ch2 fired prematurely")
	default:
	}

	// Advance another 30 mins (total 1h): ch1 should fire
	vc.Advance(30 * time.Minute)
	select {
	case fireTime := <-ch1:
		expected := initial.Add(1 * time.Hour)
		if !fireTime.Equal(expected) {
			t.Fatalf("expected fire time %v, got %v", expected, fireTime)
		}
	default:
		t.Fatal("ch1 should have fired")
	}

	if vc.WaitersCount() != 1 {
		t.Fatalf("expected 1 waiter remaining, got %d", vc.WaitersCount())
	}

	// Advance another 1h: ch2 should fire
	vc.Advance(1 * time.Hour)
	select {
	case fireTime := <-ch2:
		expected := initial.Add(2 * time.Hour)
		if !fireTime.Equal(expected) {
			t.Fatalf("expected fire time %v, got %v", expected, fireTime)
		}
	default:
		t.Fatal("ch2 should have fired")
	}

	if vc.WaitersCount() != 0 {
		t.Fatalf("expected 0 waiters remaining, got %d", vc.WaitersCount())
	}

	// Test zero duration
	chZero := vc.After(0)
	select {
	case <-chZero:
	default:
		t.Fatal("zero duration After should fire immediately")
	}

	// Test Set
	target := initial.Add(10 * time.Hour)
	chSet := vc.After(5 * time.Hour)
	vc.Set(target)
	if !vc.Now().Equal(target) {
		t.Fatalf("expected now %v, got %v", target, vc.Now())
	}
	select {
	case <-chSet:
	default:
		t.Fatal("chSet should have fired after Set")
	}
}

func TestVirtualClock_WaitForWaiter(t *testing.T) {
	initial := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	vc := NewVirtualClock(initial)

	// No timer pending yet: a concurrent After must release the blocked waiter.
	go func() {
		vc.After(1 * time.Hour)
	}()

	vc.WaitForWaiter()

	if vc.WaitersCount() != 1 {
		t.Fatalf("expected 1 waiter, got %d", vc.WaitersCount())
	}

	// With a timer already pending, WaitForWaiter returns immediately.
	vc.WaitForWaiter()
}
