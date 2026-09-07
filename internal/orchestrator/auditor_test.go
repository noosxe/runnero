package orchestrator_test

import (
	"context"
	"testing"
	"time"

	"github.com/noosxe/runnero/internal/orchestrator"
)

func TestReconciler_BootReconciliationAndAdoption(t *testing.T) {
	ctx := context.Background()

	// 1. Simulate host engine already having containers running from a prior supervisor run
	liveHostContainers := []orchestrator.RunnerStatus{
		{
			ID:        "c-running-1",
			Name:      "runnero-pool-a-111111",
			PoolName:  "pool-a",
			State:     "running",
			IPAddress: "172.20.0.2",
			SpawnedAt: time.Now().Add(-5 * time.Minute),
		},
		{
			ID:        "c-running-2",
			Name:      "runnero-pool-a-222222",
			PoolName:  "pool-a",
			State:     "running",
			IPAddress: "172.20.0.3",
			SpawnedAt: time.Now().Add(-2 * time.Minute),
		},
		{
			ID:        "c-exited-3",
			Name:      "runnero-pool-b-333333",
			PoolName:  "pool-b",
			State:     "exited",
			IPAddress: "172.20.0.4",
			SpawnedAt: time.Now().Add(-10 * time.Minute),
		},
	}

	mockProvider := orchestrator.NewMockContainerProvider()
	mockProvider.AuditRunnersFn = func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
		return liveHostContainers, nil
	}

	// 2. Supervisor boots up fresh and calls RebuildState
	reconciler := orchestrator.NewReconciler(mockProvider)
	report, err := reconciler.RebuildState(ctx)
	if err != nil {
		t.Fatalf("RebuildState failed: %v", err)
	}

	// 3. Verify all live containers were adopted into in-memory state
	if len(report.Adopted) != 3 {
		t.Fatalf("expected 3 adopted containers, got %d", len(report.Adopted))
	}
	if len(report.Active) != 2 {
		t.Errorf("expected 2 active containers, got %d", len(report.Active))
	}
	if len(report.Exited) != 1 {
		t.Errorf("expected 1 exited container, got %d", len(report.Exited))
	}
	if report.TotalTracked != 3 {
		t.Errorf("expected total tracked 3, got %d", report.TotalTracked)
	}

	poolARunners := reconciler.TrackedPoolRunners("pool-a")
	if len(poolARunners) != 2 {
		t.Errorf("expected 2 runners for pool-a, got %d", len(poolARunners))
	}

	poolBRunners := reconciler.TrackedPoolRunners("pool-b")
	if len(poolBRunners) != 1 {
		t.Errorf("expected 1 runner for pool-b, got %d", len(poolBRunners))
	}

	// 4. Second audit cycle: no duplicate spawns or re-adoptions
	secondReport, err := reconciler.Audit(ctx)
	if err != nil {
		t.Fatalf("second audit failed: %v", err)
	}
	if len(secondReport.Adopted) != 0 {
		t.Errorf("expected 0 adopted containers on second audit, got %d", len(secondReport.Adopted))
	}

	// 5. Container terminates/disappears from host engine
	liveHostContainers = liveHostContainers[1:] // remove c-running-1
	thirdReport, err := reconciler.Audit(ctx)
	if err != nil {
		t.Fatalf("third audit failed: %v", err)
	}
	if len(thirdReport.Disappeared) != 1 || thirdReport.Disappeared[0] != "c-running-1" {
		t.Errorf("expected c-running-1 disappeared, got %+v", thirdReport.Disappeared)
	}
	if len(reconciler.TrackedPoolRunners("pool-a")) != 1 {
		t.Errorf("expected 1 runner remaining in pool-a, got %d", len(reconciler.TrackedPoolRunners("pool-a")))
	}
}

func TestReconciler_TrackAndUntrack(t *testing.T) {
	mockProvider := orchestrator.NewMockContainerProvider()
	reconciler := orchestrator.NewReconciler(mockProvider)

	status := orchestrator.RunnerStatus{
		ID:       "c-100",
		Name:     "runnero-pool-x-100",
		PoolName: "pool-x",
		State:    "running",
	}

	reconciler.TrackRunner(status)
	runners := reconciler.TrackedPoolRunners("pool-x")
	if len(runners) != 1 || runners[0].ID != "c-100" {
		t.Fatalf("unexpected tracked runners: %+v", runners)
	}

	reconciler.UntrackRunner("pool-x", "c-100")
	if len(reconciler.TrackedPoolRunners("pool-x")) != 0 {
		t.Fatalf("expected pool-x to be empty after untrack")
	}
}

func TestReconciler_PeriodicStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	auditCalled := 0
	mockProvider := orchestrator.NewMockContainerProvider()
	mockProvider.AuditRunnersFn = func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
		auditCalled++
		return nil, nil
	}

	reconciler := orchestrator.NewReconciler(mockProvider)
	reportChan := make(chan orchestrator.AuditReport, 5)

	go func() {
		_ = reconciler.Start(ctx, 20*time.Millisecond, func(r orchestrator.AuditReport) {
			reportChan <- r
		})
	}()

	select {
	case <-reportChan:
		// Initial or periodic report received
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for audit report from Start loop")
	}

	cancel()
	if auditCalled == 0 {
		t.Errorf("expected audit to be called at least once")
	}
}
