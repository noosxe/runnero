package orchestrator_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/noosxe/runnero/internal/orchestrator"
)

func TestReconciler_BootReconciliationAndAdoption(t *testing.T) {
	ctx := context.Background()

	// 1. Simulate host engine already having containers running from a prior supervisor run
	liveHostContainers := []orchestrator.RunnerStatus{
		{PoolID: 100,
			ID:        "c-running-1",
			Name:      "runnero-pool-a-111111",
			PoolName:  "pool-a",
			State:     "running",
			IPAddress: "172.20.0.2",
			SpawnedAt: time.Now().Add(-5 * time.Minute),
		},
		{PoolID: 100,
			ID:        "c-running-2",
			Name:      "runnero-pool-a-222222",
			PoolName:  "pool-a",
			State:     "running",
			IPAddress: "172.20.0.3",
			SpawnedAt: time.Now().Add(-2 * time.Minute),
		},
		{PoolID: 101,
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

	poolARunners := reconciler.TrackedPoolRunners(100)
	if len(poolARunners) != 2 {
		t.Errorf("expected 2 runners for pool-a, got %d", len(poolARunners))
	}

	poolBRunners := reconciler.TrackedPoolRunners(101)
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
	if len(reconciler.TrackedPoolRunners(100)) != 1 {
		t.Errorf("expected 1 runner remaining in pool-a, got %d", len(reconciler.TrackedPoolRunners(100)))
	}
}

func TestReconciler_TrackAndUntrack(t *testing.T) {
	mockProvider := orchestrator.NewMockContainerProvider()
	reconciler := orchestrator.NewReconciler(mockProvider)

	status := orchestrator.RunnerStatus{
		PoolID:   102,
		ID:       "c-100",
		Name:     "runnero-pool-x-100",
		PoolName: "pool-x",
		State:    "running",
	}

	reconciler.TrackRunner(status)
	runners := reconciler.TrackedPoolRunners(102)
	if len(runners) != 1 || runners[0].ID != "c-100" {
		t.Fatalf("unexpected tracked runners: %+v", runners)
	}

	reconciler.UntrackRunner(102, "c-100")
	if len(reconciler.TrackedPoolRunners(102)) != 0 {
		t.Fatalf("expected pool-x to be empty after untrack")
	}
}

// TestReconciler_LegacyNameOnlyContainersAdoptedByResolver verifies that
// TestReconciler_TrackedPoolRunnersStableOrder: the snapshot comes from a
// Go map, so without sorting every call returned the runners in a different
// order and UI tables reshuffled on each poll. The snapshot must be
// deterministically ordered by runner name.
func TestReconciler_TrackedPoolRunnersStableOrder(t *testing.T) {
	mockProvider := orchestrator.NewMockContainerProvider()
	reconciler := orchestrator.NewReconciler(mockProvider)

	names := []string{
		"runnero-pool-x-delta", "runnero-pool-x-alpha", "runnero-pool-x-charlie",
		"runnero-pool-x-echo", "runnero-pool-x-bravo", "runnero-pool-x-foxtrot",
	}
	for i, name := range names {
		reconciler.TrackRunner(orchestrator.RunnerStatus{
			PoolID:   103,
			ID:       fmt.Sprintf("c-%d", i),
			Name:     name,
			PoolName: "pool-x",
			State:    "running",
		})
	}

	first := reconciler.TrackedPoolRunners(103)
	if len(first) != len(names) {
		t.Fatalf("expected %d runners, got %d", len(names), len(first))
	}
	for i := 1; i < len(first); i++ {
		if first[i-1].Name >= first[i].Name {
			t.Fatalf("snapshot not sorted by name: %q before %q", first[i-1].Name, first[i].Name)
		}
	}

	// Map iteration randomizes per call; repeated snapshots must agree.
	for attempt := 0; attempt < 25; attempt++ {
		again := reconciler.TrackedPoolRunners(103)
		for i := range again {
			if again[i].Name != first[i].Name {
				t.Fatalf("snapshot order changed at %d: %q vs %q", i, again[i].Name, first[i].Name)
			}
		}
	}
}

// containers spawned before the pool-id label existed (RUN-126) — whose only
// pool association is the spawn-time name label — are promoted to their
// pool's database id at audit time, preserving boot adoption of in-flight
// runners across the upgrade (docs/03 §2). Unresolvable names land in the
// zero bucket and are left to orphan handling.
func TestReconciler_LegacyNameOnlyContainersAdoptedByResolver(t *testing.T) {
	ctx := context.Background()
	mockProvider := orchestrator.NewMockContainerProvider()
	mockProvider.AuditRunnersFn = func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
		return []orchestrator.RunnerStatus{
			{ID: "c-legacy", Name: "runnero-ci-main-111", PoolName: "ci-main", State: "running"}, // no PoolID label
			{ID: "c-unknown", Name: "runnero-gone-222", PoolName: "deleted-pool", State: "running"},
		}, nil
	}

	reconciler := orchestrator.NewReconciler(mockProvider)
	reconciler.SetPoolNameResolver(func(name string) (int64, bool) {
		if name == "ci-main" {
			return 7, true
		}
		return 0, false
	})

	if _, err := reconciler.RebuildState(ctx); err != nil {
		t.Fatalf("RebuildState failed: %v", err)
	}

	promoted := reconciler.TrackedPoolRunners(7)
	if len(promoted) != 1 || promoted[0].ID != "c-legacy" || promoted[0].PoolID != 7 {
		t.Fatalf("legacy container must be adopted under its pool id: %+v", promoted)
	}
	unresolved := reconciler.TrackedPoolRunners(0)
	if len(unresolved) != 1 || unresolved[0].ID != "c-unknown" {
		t.Fatalf("unresolvable container must land in the zero bucket: %+v", unresolved)
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
