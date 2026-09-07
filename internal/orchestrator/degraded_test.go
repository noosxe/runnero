package orchestrator_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/noosxe/runnero/internal/orchestrator"
)

func TestDockerHealthTracker_DegradedAndRecovery(t *testing.T) {
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	var capturedAlert orchestrator.DockerAlert
	var alertMu sync.Mutex

	tracker := orchestrator.NewDockerHealthTracker("test-host", func(a orchestrator.DockerAlert) {
		alertMu.Lock()
		capturedAlert = a
		alertMu.Unlock()
	})
	tracker.SetNowFn(func() time.Time { return now })

	// 1. Initial healthy state
	if tracker.IsDegraded() {
		t.Fatalf("expected healthy initial state")
	}
	if err := tracker.CanSpawn(); err != nil {
		t.Fatalf("expected CanSpawn to succeed, got %v", err)
	}
	if tracker.Status() != "ok" {
		t.Errorf("expected status 'ok', got %q", tracker.Status())
	}

	// 2. First failure: enters degraded mode, rate-limits log
	_, shouldLog, alertRaised := tracker.RecordFailure(errors.New("connection refused"))
	if !tracker.IsDegraded() {
		t.Fatalf("expected degraded state after failure")
	}
	if !shouldLog {
		t.Errorf("expected shouldLog=true on first failure")
	}
	if alertRaised {
		t.Errorf("alert should not be raised on initial failure")
	}
	if err := tracker.CanSpawn(); !errors.Is(err, orchestrator.ErrDaemonDegraded) {
		t.Fatalf("expected ErrDaemonDegraded, got: %v", err)
	}
	if tracker.Status() != "degraded" {
		t.Errorf("expected status 'degraded', got %q", tracker.Status())
	}

	// 3. Subsequent failure within cooldown: should not log (anti-spam)
	now = now.Add(5 * time.Second)
	_, shouldLog, _ = tracker.RecordFailure(errors.New("connection refused"))
	if shouldLog {
		t.Errorf("expected shouldLog=false within cooldown window")
	}

	// 4. Failure after log cooldown (30s): should log again
	now = now.Add(35 * time.Second)
	_, shouldLog, _ = tracker.RecordFailure(errors.New("connection refused"))
	if !shouldLog {
		t.Errorf("expected shouldLog=true after cooldown expired")
	}

	// 5. Unreachable for 4 minutes: no alert yet
	now = now.Add(3 * time.Minute)
	_, _, alertRaised = tracker.RecordFailure(errors.New("connection refused"))
	if alertRaised {
		t.Errorf("alert should not be raised at <5 minutes")
	}

	// 6. Unreachable exceeds 5 minutes: alert raised for UI (OQ #5)
	now = now.Add(2 * time.Minute) // total ~5m40s down
	_, _, alertRaised = tracker.RecordFailure(errors.New("connection refused"))
	if !alertRaised {
		t.Errorf("expected alertRaised=true after >5 minutes down")
	}
	// Give alert goroutine a moment to populate capturedAlert
	time.Sleep(20 * time.Millisecond)
	alertMu.Lock()
	alert := capturedAlert
	alertMu.Unlock()
	if alert.HostID != "test-host" || alert.Duration < 5*time.Minute {
		t.Errorf("unexpected captured alert: %+v", alert)
	}

	// Subsequent failure does not re-raise duplicate alert
	now = now.Add(1 * time.Minute)
	_, _, alertRaised = tracker.RecordFailure(errors.New("connection refused"))
	if alertRaised {
		t.Errorf("expected alertRaised=false on subsequent failure once already alerted")
	}

	// 7. Daemon recovers (acceptance: stays alive, recovers when daemon returns)
	recovered := tracker.RecordSuccess()
	if !recovered {
		t.Fatalf("expected RecordSuccess to report recovered=true")
	}
	if tracker.IsDegraded() {
		t.Fatalf("expected healthy state after recovery")
	}
	if err := tracker.CanSpawn(); err != nil {
		t.Fatalf("expected CanSpawn to succeed after recovery, got %v", err)
	}
	if tracker.Status() != "ok" {
		t.Errorf("expected status 'ok' after recovery, got %q", tracker.Status())
	}

	// RecordSuccess again when already healthy returns recovered=false
	if tracker.RecordSuccess() {
		t.Errorf("expected recovered=false when already healthy")
	}
}
