package orchestrator_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/noosxe/runnero/internal/db"
	"github.com/noosxe/runnero/internal/orchestrator"
	"github.com/noosxe/runnero/internal/provider"
	"github.com/noosxe/runnero/internal/webhook"
)

type mockMultiTargetDB struct {
	pools   []db.RunnerPool
	targets map[int64][]db.PoolTarget
}

func (m *mockMultiTargetDB) ListRunnerPools(ctx context.Context) ([]db.RunnerPool, error) {
	return m.pools, nil
}

func (m *mockMultiTargetDB) ListPoolTargetsByPoolId(ctx context.Context, poolID int64) ([]db.PoolTarget, error) {
	return m.targets[poolID], nil
}

func TestMatchPoolForEventWithTargets(t *testing.T) {
	pool := db.RunnerPool{
		ID:            10,
		Name:          "multi-repo-pool",
		Provider:      "github",
		RepositoryUrl: "https://github.com/acme/repo-alpha",
		Scope:         "repo",
		Labels:        `["self-hosted","linux"]`,
	}

	poolTargets := map[int64][]string{
		10: {
			"https://github.com/acme/repo-alpha",
			"https://github.com/acme/repo-beta",
			"https://github.com/acme/repo-gamma",
		},
	}

	eventBeta := &webhook.WorkflowJobEvent{
		Action: "queued",
		Repository: webhook.RepositoryPayload{
			FullName: "acme/repo-beta",
			HTMLURL:  "https://github.com/acme/repo-beta",
		},
		WorkflowJob: webhook.WorkflowJobPayload{
			ID:     101,
			Labels: []string{"self-hosted", "linux"},
		},
	}

	matchedPool, matchedURL := orchestrator.MatchPoolForEventWithTargets(
		[]db.RunnerPool{pool},
		poolTargets,
		"github",
		eventBeta,
	)

	if matchedPool == nil {
		t.Fatalf("expected pool to match repo-beta, got nil")
	}
	if matchedPool.ID != 10 {
		t.Errorf("expected pool ID 10, got %d", matchedPool.ID)
	}
	if matchedURL != "https://github.com/acme/repo-beta" {
		t.Errorf("expected matched URL https://github.com/acme/repo-beta, got %q", matchedURL)
	}

	// Test repo not in targets
	eventDelta := &webhook.WorkflowJobEvent{
		Action: "queued",
		Repository: webhook.RepositoryPayload{
			FullName: "acme/repo-delta",
			HTMLURL:  "https://github.com/acme/repo-delta",
		},
		WorkflowJob: webhook.WorkflowJobPayload{
			ID:     102,
			Labels: []string{"self-hosted", "linux"},
		},
	}

	matchedDelta, _ := orchestrator.MatchPoolForEventWithTargets(
		[]db.RunnerPool{pool},
		poolTargets,
		"github",
		eventDelta,
	)
	if matchedDelta != nil {
		t.Errorf("expected delta to not match pool, got %v", matchedDelta.Name)
	}
}

func TestPoolController_MultiTargetStandbyDistribution(t *testing.T) {
	ctx := context.Background()

	mockDB := &mockMultiTargetDB{
		pools: []db.RunnerPool{
			{
				ID:             1,
				Name:           "shared-pool",
				Provider:       "github",
				RepositoryUrl:  "https://github.com/acme/repo-1",
				Scope:          "repo",
				AuthProfileID:  1,
				MinIdleRunners: 3,
				MaxConcurrency: 5,
				Labels:         `["self-hosted"]`,
			},
		},
		targets: map[int64][]db.PoolTarget{
			1: {
				{PoolID: 1, TargetUrl: "https://github.com/acme/repo-1"},
				{PoolID: 1, TargetUrl: "https://github.com/acme/repo-2"},
				{PoolID: 1, TargetUrl: "https://github.com/acme/repo-3"},
			},
		},
	}

	gitProv := &mockGitProvider{}
	resolver := &mockGitProviderResolver{
		providers: map[int64]provider.GitProvider{1: gitProv},
	}

	var mu sync.Mutex
	var spawnedConfigs []orchestrator.RunnerConfig
	mockEngine := &orchestrator.MockContainerProvider{
		SpawnRunnerFn: func(ctx context.Context, config orchestrator.RunnerConfig) (string, error) {
			mu.Lock()
			spawnedConfigs = append(spawnedConfigs, config)
			id := fmt.Sprintf("cnt-%d", len(spawnedConfigs))
			mu.Unlock()
			return id, nil
		},
	}

	controller := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:               mockDB,
		ContainerEngine:  mockEngine,
		ProviderResolver: resolver,
	})

	if err := controller.Boot(ctx); err != nil {
		t.Fatalf("Boot failed: %v", err)
	}

	if len(spawnedConfigs) != 3 {
		t.Fatalf("expected 3 spawned runners, got %d", len(spawnedConfigs))
	}

	// Verify each target received a runner
	repoCount := make(map[string]int)
	for _, cfg := range spawnedConfigs {
		repoCount[cfg.RepoURL]++
	}

	if repoCount["https://github.com/acme/repo-1"] != 1 {
		t.Errorf("expected 1 runner for repo-1, got %d", repoCount["https://github.com/acme/repo-1"])
	}
	if repoCount["https://github.com/acme/repo-2"] != 1 {
		t.Errorf("expected 1 runner for repo-2, got %d", repoCount["https://github.com/acme/repo-2"])
	}
	if repoCount["https://github.com/acme/repo-3"] != 1 {
		t.Errorf("expected 1 runner for repo-3, got %d", repoCount["https://github.com/acme/repo-3"])
	}
}

func TestPoolController_MultiTargetForgejoPolling(t *testing.T) {
	ctx := context.Background()

	mockDB := &mockMultiTargetDB{
		pools: []db.RunnerPool{
			{
				ID:             2,
				Name:           "forgejo-multi",
				Provider:       "forgejo",
				RepositoryUrl:  "https://forgejo.example.com/org/repo-a",
				Scope:          "repo",
				AuthProfileID:  2,
				MinIdleRunners: 0,
				MaxConcurrency: 4,
				Labels:         `["self-hosted"]`,
			},
		},
		targets: map[int64][]db.PoolTarget{
			2: {
				{PoolID: 2, TargetUrl: "https://forgejo.example.com/org/repo-a"},
				{PoolID: 2, TargetUrl: "https://forgejo.example.com/org/repo-b"},
			},
		},
	}

	gitProv := &mockGitProvider{
		scalingMode: provider.ScalingPolling,
		queuedJobs:  1, // 1 job returned per PollQueuedJobs call => 2 total across 2 targets
	}
	resolver := &mockGitProviderResolver{
		providers: map[int64]provider.GitProvider{2: gitProv},
	}

	var mu sync.Mutex
	var spawnedConfigs []orchestrator.RunnerConfig
	mockEngine := &orchestrator.MockContainerProvider{
		SpawnRunnerFn: func(ctx context.Context, config orchestrator.RunnerConfig) (string, error) {
			mu.Lock()
			spawnedConfigs = append(spawnedConfigs, config)
			id := fmt.Sprintf("cnt-%d", len(spawnedConfigs))
			mu.Unlock()
			return id, nil
		},
	}

	controller := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:               mockDB,
		ContainerEngine:  mockEngine,
		ProviderResolver: resolver,
	})

	if err := controller.Boot(ctx); err != nil {
		t.Fatalf("Boot failed: %v", err)
	}

	// Should have polled both targets (2 poll calls)
	if gitProv.pollCalls != 2 {
		t.Errorf("expected 2 poll calls across 2 targets, got %d", gitProv.pollCalls)
	}

	// Total queued jobs = 1 + 1 = 2, so 2 runners spawned up to max_concurrency
	if len(spawnedConfigs) != 2 {
		t.Errorf("expected 2 spawned runners from polling, got %d", len(spawnedConfigs))
	}
}
