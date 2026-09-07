package orchestrator_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/noosxe/runnero/internal/db"
	"github.com/noosxe/runnero/internal/orchestrator"
	"github.com/noosxe/runnero/internal/provider"
	"github.com/noosxe/runnero/internal/webhook"
)

func TestNormalizeRepositoryURL(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"https://github.com/owner/repo.git/", "https://github.com/owner/repo"},
		{"https://GITHUB.COM/Owner/Repo", "https://github.com/owner/repo"},
		{"http://gitea.local:3000/my-org/my-repo.git", "http://gitea.local:3000/my-org/my-repo"},
		{"https://github.com/my-org/", "https://github.com/my-org"},
		{"https://github.com/", "https://github.com"},
		{"", ""},
		{"   ", ""},
	}

	for _, tc := range tests {
		got := orchestrator.NormalizeRepositoryURL(tc.input)
		if got != tc.want {
			t.Errorf("NormalizeRepositoryURL(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestLabelsMatch(t *testing.T) {
	tests := []struct {
		poolLabels string
		jobLabels  []string
		want       bool
	}{
		{`["self-hosted","linux","arm64"]`, []string{"self-hosted", "linux"}, true},
		{`["self-hosted","linux"]`, []string{"self-hosted", "linux", "arm64"}, false},
		{`["self-hosted","linux"]`, []string{}, true},
		{"self-hosted, linux, arm64", []string{"ARM64", "Linux"}, true},
		{"self-hosted, linux", []string{"gpu"}, false},
		{"", []string{"self-hosted"}, true}, // defaults to self-hosted, linux
		{"", []string{"windows"}, false},
	}

	for i, tc := range tests {
		got := orchestrator.LabelsMatch(tc.poolLabels, tc.jobLabels)
		if got != tc.want {
			t.Errorf("[%d] LabelsMatch(%q, %v) = %v, want %v", i, tc.poolLabels, tc.jobLabels, got, tc.want)
		}
	}
}

func TestMatchPoolForEvent(t *testing.T) {
	repoPool := db.RunnerPool{
		ID:            1,
		Name:          "repo-pool",
		Provider:      "github",
		RepositoryUrl: "https://github.com/octocat/hello-world",
		Scope:         "repo",
		Labels:        `["self-hosted","linux"]`,
	}
	orgPool := db.RunnerPool{
		ID:            2,
		Name:          "org-pool",
		Provider:      "github",
		RepositoryUrl: "https://github.com/octocat",
		Scope:         "org",
		Labels:        `["self-hosted","linux"]`,
	}
	globalPool := db.RunnerPool{
		ID:            3,
		Name:          "global-pool",
		Provider:      "github",
		RepositoryUrl: "https://github.com",
		Scope:         "global",
		Labels:        `["self-hosted","linux"]`,
	}
	giteaPool := db.RunnerPool{
		ID:            4,
		Name:          "gitea-pool",
		Provider:      "gitea",
		RepositoryUrl: "https://gitea.example.com/octocat/hello-world",
		Scope:         "repo",
		Labels:        `["self-hosted","linux"]`,
	}

	pools := []db.RunnerPool{globalPool, orgPool, repoPool, giteaPool}

	// 1. Repo match priority (repo > org > global)
	event1 := &webhook.WorkflowJobEvent{
		Action: "queued",
		Repository: webhook.RepositoryPayload{
			FullName: "octocat/hello-world",
			HTMLURL:  "https://github.com/octocat/hello-world",
		},
		WorkflowJob: webhook.WorkflowJobPayload{
			ID:     101,
			Labels: []string{"self-hosted"},
		},
	}
	matched := orchestrator.MatchPoolForEvent(pools, "github", event1)
	if matched == nil || matched.Name != "repo-pool" {
		t.Fatalf("expected repo-pool, got %+v", matched)
	}

	// 2. Org match when repo pool doesn't match
	event2 := &webhook.WorkflowJobEvent{
		Action: "queued",
		Repository: webhook.RepositoryPayload{
			FullName: "octocat/other-repo",
			HTMLURL:  "https://github.com/octocat/other-repo",
		},
		WorkflowJob: webhook.WorkflowJobPayload{
			ID:     102,
			Labels: []string{"self-hosted"},
		},
	}
	matched2 := orchestrator.MatchPoolForEvent(pools, "github", event2)
	if matched2 == nil || matched2.Name != "org-pool" {
		t.Fatalf("expected org-pool, got %+v", matched2)
	}

	// 3. Global match when neither repo nor org pool matches
	event3 := &webhook.WorkflowJobEvent{
		Action: "queued",
		Repository: webhook.RepositoryPayload{
			FullName: "other-org/some-repo",
			HTMLURL:  "https://github.com/other-org/some-repo",
		},
		WorkflowJob: webhook.WorkflowJobPayload{
			ID:     103,
			Labels: []string{"self-hosted"},
		},
	}
	matched3 := orchestrator.MatchPoolForEvent(pools, "github", event3)
	if matched3 == nil || matched3.Name != "global-pool" {
		t.Fatalf("expected global-pool, got %+v", matched3)
	}

	// 4. Provider filtering (gitea)
	event4 := &webhook.WorkflowJobEvent{
		Action: "queued",
		Repository: webhook.RepositoryPayload{
			FullName: "octocat/hello-world",
			HTMLURL:  "https://gitea.example.com/octocat/hello-world",
		},
		WorkflowJob: webhook.WorkflowJobPayload{
			ID:     104,
			Labels: []string{"self-hosted"},
		},
	}
	matched4 := orchestrator.MatchPoolForEvent(pools, "gitea", event4)
	if matched4 == nil || matched4.Name != "gitea-pool" {
		t.Fatalf("expected gitea-pool, got %+v", matched4)
	}

	// 5. Label disqualification
	event5 := &webhook.WorkflowJobEvent{
		Action: "queued",
		Repository: webhook.RepositoryPayload{
			FullName: "octocat/hello-world",
			HTMLURL:  "https://github.com/octocat/hello-world",
		},
		WorkflowJob: webhook.WorkflowJobPayload{
			ID:     105,
			Labels: []string{"self-hosted", "arm64"},
		},
	}
	matched5 := orchestrator.MatchPoolForEvent([]db.RunnerPool{repoPool}, "github", event5)
	if matched5 != nil {
		t.Fatalf("expected nil when labels don't match, got %+v", matched5)
	}
}

func TestPoolController_HandleWorkflowJob_Queued_Success(t *testing.T) {
	ctx := context.Background()

	pool := db.RunnerPool{
		ID:             1,
		Name:           "webhook-pool",
		Provider:       "github",
		RepositoryUrl:  "https://github.com/test-org/test-repo",
		Scope:          "repo",
		AuthProfileID:  10,
		MinIdleRunners: 1,
		MaxConcurrency: 5,
		Labels:         `["self-hosted","linux"]`,
		RunnerImage:    "ghcr.io/noosxe/runnero:latest",
		AllowDocker:    true,
		CpuLimit:       sql.NullString{String: "2", Valid: true},
		MemoryLimit:    sql.NullString{String: "4g", Valid: true},
	}

	repo := &mockPoolRepo{pools: []db.RunnerPool{pool}}
	gitProv := &mockGitProvider{}
	resolver := &mockGitProviderResolver{
		providers: map[int64]provider.GitProvider{10: gitProv},
	}

	spawnCount := 0
	mockEngine := &orchestrator.MockContainerProvider{
		SpawnRunnerFn: func(ctx context.Context, config orchestrator.RunnerConfig) (string, error) {
			spawnCount++
			return fmt.Sprintf("container-%d", spawnCount), nil
		},
		AuditRunnersFn: func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
			return nil, nil
		},
		PingFn: func(ctx context.Context) error {
			return nil
		},
	}

	controller := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:               repo,
		ContainerEngine:  mockEngine,
		ProviderResolver: resolver,
		GlobalMaxRunners: 10,
		Interval:         time.Hour, // long interval so periodic tick does not interfere
	})

	// Boot provisions min_idle_runners (1 runner)
	if err := controller.Boot(ctx); err != nil {
		t.Fatalf("boot failed: %v", err)
	}
	if spawnCount != 1 {
		t.Fatalf("expected 1 initial runner, got %d", spawnCount)
	}

	// Webhook queued event arrives -> should immediately provision a 2nd runner
	evt := &webhook.WorkflowJobEvent{
		Action: "queued",
		Repository: webhook.RepositoryPayload{
			FullName: "test-org/test-repo",
			HTMLURL:  "https://github.com/test-org/test-repo",
		},
		WorkflowJob: webhook.WorkflowJobPayload{
			ID:     501,
			Labels: []string{"self-hosted", "linux"},
		},
	}

	if err := controller.HandleWorkflowJob(ctx, "github", evt); err != nil {
		t.Fatalf("HandleWorkflowJob failed: %v", err)
	}

	if spawnCount != 2 {
		t.Fatalf("expected 2 runners spawned after queued event, got %d", spawnCount)
	}

	if controller.TotalActiveRunners() != 2 {
		t.Fatalf("expected 2 active runners, got %d", controller.TotalActiveRunners())
	}
}

func TestPoolController_HandleWorkflowJob_Queued_MaxConcurrencyReached(t *testing.T) {
	ctx := context.Background()

	pool := db.RunnerPool{
		ID:             1,
		Name:           "max-pool",
		Provider:       "github",
		RepositoryUrl:  "https://github.com/test-org/test-repo",
		Scope:          "repo",
		AuthProfileID:  10,
		MinIdleRunners: 2,
		MaxConcurrency: 2, // max is 2
		Labels:         `["self-hosted","linux"]`,
		RunnerImage:    "ghcr.io/noosxe/runnero:latest",
	}

	repo := &mockPoolRepo{pools: []db.RunnerPool{pool}}
	gitProv := &mockGitProvider{}
	resolver := &mockGitProviderResolver{
		providers: map[int64]provider.GitProvider{10: gitProv},
	}

	spawnCount := 0
	mockEngine := &orchestrator.MockContainerProvider{
		SpawnRunnerFn: func(ctx context.Context, config orchestrator.RunnerConfig) (string, error) {
			spawnCount++
			return fmt.Sprintf("container-%d", spawnCount), nil
		},
		AuditRunnersFn: func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
			return nil, nil
		},
		PingFn: func(ctx context.Context) error {
			return nil
		},
	}

	controller := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:               repo,
		ContainerEngine:  mockEngine,
		ProviderResolver: resolver,
		GlobalMaxRunners: 10,
		Interval:         time.Hour,
	})

	if err := controller.Boot(ctx); err != nil {
		t.Fatalf("boot failed: %v", err)
	}
	if spawnCount != 2 {
		t.Fatalf("expected 2 initial runners, got %d", spawnCount)
	}

	// Webhook queued event arrives -> activeCount is already 2 (equal to max_concurrency) -> skip spawn
	evt := &webhook.WorkflowJobEvent{
		Action: "queued",
		Repository: webhook.RepositoryPayload{
			FullName: "test-org/test-repo",
			HTMLURL:  "https://github.com/test-org/test-repo",
		},
		WorkflowJob: webhook.WorkflowJobPayload{
			ID:     502,
			Labels: []string{"self-hosted"},
		},
	}

	if err := controller.HandleWorkflowJob(ctx, "github", evt); err != nil {
		t.Fatalf("HandleWorkflowJob failed: %v", err)
	}

	if spawnCount != 2 {
		t.Fatalf("expected spawnCount to remain 2 when max concurrency is reached, got %d", spawnCount)
	}
}

func TestPoolController_HandleWorkflowJob_Queued_GlobalQuotaSaturated(t *testing.T) {
	ctx := context.Background()

	pool := db.RunnerPool{
		ID:             1,
		Name:           "quota-pool",
		Provider:       "github",
		RepositoryUrl:  "https://github.com/test-org/test-repo",
		Scope:          "repo",
		AuthProfileID:  10,
		MinIdleRunners: 2,
		MaxConcurrency: 5,
		Labels:         `["self-hosted","linux"]`,
		RunnerImage:    "ghcr.io/noosxe/runnero:latest",
	}

	repo := &mockPoolRepo{pools: []db.RunnerPool{pool}}
	gitProv := &mockGitProvider{}
	resolver := &mockGitProviderResolver{
		providers: map[int64]provider.GitProvider{10: gitProv},
	}

	spawnCount := 0
	mockEngine := &orchestrator.MockContainerProvider{
		SpawnRunnerFn: func(ctx context.Context, config orchestrator.RunnerConfig) (string, error) {
			spawnCount++
			return fmt.Sprintf("container-%d", spawnCount), nil
		},
		TerminateRunnerFn: func(ctx context.Context, id string) error {
			return nil
		},
		AuditRunnersFn: func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
			return nil, nil
		},
		PingFn: func(ctx context.Context) error {
			return nil
		},
	}

	// GlobalMaxRunners is 2, and boot will spawn 2 runners, saturating global quota
	controller := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:               repo,
		ContainerEngine:  mockEngine,
		ProviderResolver: resolver,
		GlobalMaxRunners: 2,
		Interval:         time.Hour,
	})

	if err := controller.Boot(ctx); err != nil {
		t.Fatalf("boot failed: %v", err)
	}
	if spawnCount != 2 {
		t.Fatalf("expected 2 initial runners, got %d", spawnCount)
	}

	// Webhook queued event arrives -> global quota saturated -> request is enqueued internally
	evt := &webhook.WorkflowJobEvent{
		Action: "queued",
		Repository: webhook.RepositoryPayload{
			FullName: "test-org/test-repo",
			HTMLURL:  "https://github.com/test-org/test-repo",
		},
		WorkflowJob: webhook.WorkflowJobPayload{
			ID:     503,
			Labels: []string{"self-hosted"},
		},
	}

	if err := controller.HandleWorkflowJob(ctx, "github", evt); err != nil {
		t.Fatalf("HandleWorkflowJob failed: %v", err)
	}

	if spawnCount != 2 {
		t.Fatalf("expected spawnCount to remain 2 when global quota is saturated, got %d", spawnCount)
	}
	if controller.QueueLengthForPool("quota-pool") != 1 {
		t.Fatalf("expected 1 request in internal queue, got %d", controller.QueueLengthForPool("quota-pool"))
	}

	// Simulate container termination event -> frees capacity and drains queue
	termEvent := orchestrator.ContainerEvent{
		Action:      "die",
		ContainerID: "container-1",
		PoolName:    "quota-pool",
		ExitCode:    0,
	}
	if err := controller.HandleContainerEvent(ctx, termEvent); err != nil {
		t.Fatalf("HandleContainerEvent failed: %v", err)
	}

	// Queue should now be drained and a new runner spawned
	if controller.QueueLengthForPool("quota-pool") != 0 {
		t.Fatalf("expected queue to be drained, remaining: %d", controller.QueueLengthForPool("quota-pool"))
	}
	if spawnCount < 3 {
		t.Fatalf("expected queued runner to be spawned upon capacity release, got spawnCount=%d", spawnCount)
	}
}

func TestPoolController_HandleWorkflowJob_InProgressAndCompleted(t *testing.T) {
	ctx := context.Background()

	pool := db.RunnerPool{
		ID:             1,
		Name:           "status-pool",
		Provider:       "github",
		RepositoryUrl:  "https://github.com/test-org/test-repo",
		Scope:          "repo",
		AuthProfileID:  10,
		MinIdleRunners: 1,
		MaxConcurrency: 5,
		Labels:         `["self-hosted","linux"]`,
		RunnerImage:    "ghcr.io/noosxe/runnero:latest",
	}

	repo := &mockPoolRepo{pools: []db.RunnerPool{pool}}
	gitProv := &mockGitProvider{}
	resolver := &mockGitProviderResolver{
		providers: map[int64]provider.GitProvider{10: gitProv},
	}

	var spawnedName string
	mockEngine := &orchestrator.MockContainerProvider{
		SpawnRunnerFn: func(ctx context.Context, config orchestrator.RunnerConfig) (string, error) {
			spawnedName = config.Name
			return "container-status-1", nil
		},
		AuditRunnersFn: func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
			return nil, nil
		},
		PingFn: func(ctx context.Context) error {
			return nil
		},
	}

	controller := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:               repo,
		ContainerEngine:  mockEngine,
		ProviderResolver: resolver,
		GlobalMaxRunners: 10,
		Interval:         time.Hour,
	})

	if err := controller.Boot(ctx); err != nil {
		t.Fatalf("boot failed: %v", err)
	}

	active, idle := controller.PoolStats("status-pool")
	if active != 0 || idle != 1 {
		t.Fatalf("expected 0 active, 1 idle; got active=%d, idle=%d", active, idle)
	}

	// in_progress event marks runner busy
	evtProgress := &webhook.WorkflowJobEvent{
		Action: "in_progress",
		WorkflowJob: webhook.WorkflowJobPayload{
			ID:         601,
			RunnerName: spawnedName,
		},
	}
	if err := controller.HandleWorkflowJob(ctx, "github", evtProgress); err != nil {
		t.Fatalf("HandleWorkflowJob in_progress failed: %v", err)
	}

	active, idle = controller.PoolStats("status-pool")
	if active != 1 || idle != 0 {
		t.Fatalf("expected 1 active, 0 idle after in_progress; got active=%d, idle=%d", active, idle)
	}

	// completed event marks runner not busy
	evtCompleted := &webhook.WorkflowJobEvent{
		Action: "completed",
		WorkflowJob: webhook.WorkflowJobPayload{
			ID:         601,
			RunnerName: spawnedName,
		},
	}
	if err := controller.HandleWorkflowJob(ctx, "github", evtCompleted); err != nil {
		t.Fatalf("HandleWorkflowJob completed failed: %v", err)
	}

	active, idle = controller.PoolStats("status-pool")
	if active != 0 || idle != 1 {
		t.Fatalf("expected 0 active, 1 idle after completed; got active=%d, idle=%d", active, idle)
	}
}
