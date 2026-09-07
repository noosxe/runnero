package orchestrator_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/noosxe/runnero/internal/orchestrator"
)

func TestMockContainerProvider(t *testing.T) {
	ctx := context.Background()
	mock := orchestrator.NewMockContainerProvider()

	// 1. Verify interface satisfaction
	var _ orchestrator.ContainerProvider = mock

	// 2. Ping
	if err := mock.Ping(ctx); err != nil {
		t.Fatalf("unexpected ping error: %v", err)
	}

	mock.PingFn = func(ctx context.Context) error {
		return errors.New("daemon down")
	}
	if err := mock.Ping(ctx); err == nil || err.Error() != "daemon down" {
		t.Fatalf("expected daemon down error, got: %v", err)
	}

	// 3. SpawnRunner
	cfg := orchestrator.RunnerConfig{
		Name:         "test-runner-1",
		RepoURL:      "https://github.com/org/repo",
		Token:        "tok-123",
		PoolName:     "pool-a",
		DockerHostID: "host-1",
	}

	id, err := mock.SpawnRunner(ctx, cfg)
	if err != nil {
		t.Fatalf("SpawnRunner failed: %v", err)
	}
	if id != "mock-container-runner-test-runner-1" {
		t.Errorf("unexpected runner ID: %q", id)
	}
	if len(mock.SpawnedRunners) != 1 || mock.SpawnedRunners[0].Name != "test-runner-1" {
		t.Errorf("expected 1 recorded runner, got %+v", mock.SpawnedRunners)
	}

	// 4. SpawnTask
	taskCfg := orchestrator.RunnerConfig{
		Name:     "renovate-task-1",
		PoolName: "renovate-pool",
	}
	taskID, err := mock.SpawnTask(ctx, taskCfg)
	if err != nil {
		t.Fatalf("SpawnTask failed: %v", err)
	}
	if taskID != "mock-container-task-renovate-task-1" {
		t.Errorf("unexpected task ID: %q", taskID)
	}

	// 5. TerminateRunner
	if err := mock.TerminateRunner(ctx, id); err != nil {
		t.Fatalf("TerminateRunner failed: %v", err)
	}
	if len(mock.TerminatedIDs) != 1 || mock.TerminatedIDs[0] != id {
		t.Errorf("expected 1 terminated ID, got %+v", mock.TerminatedIDs)
	}

	// 6. AuditRunners
	mock.AuditRunnersFn = func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
		return []orchestrator.RunnerStatus{
			{
				ID:        "c-1",
				Name:      "runner-1",
				PoolName:  "pool-a",
				State:     "running",
				SpawnedAt: time.Now(),
			},
		}, nil
	}
	statuses, err := mock.AuditRunners(ctx)
	if err != nil {
		t.Fatalf("AuditRunners failed: %v", err)
	}
	if len(statuses) != 1 || statuses[0].Name != "runner-1" {
		t.Errorf("unexpected statuses: %+v", statuses)
	}

	// 7. PruneExitedContainers
	if err := mock.PruneExitedContainers(ctx); err != nil {
		t.Fatalf("PruneExitedContainers failed: %v", err)
	}

	// 8. Close
	if err := mock.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}
