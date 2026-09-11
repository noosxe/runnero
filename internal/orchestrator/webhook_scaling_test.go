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
		ID:            162,
		Name:          "repo-pool",
		Provider:      "github",
		RepositoryUrl: "https://github.com/octocat/hello-world",
		Scope:         "repo",
		Labels:        `["self-hosted","linux"]`,
	}
	orgPool := db.RunnerPool{
		ID:            163,
		Name:          "org-pool",
		Provider:      "github",
		RepositoryUrl: "https://github.com/octocat",
		Scope:         "org",
		Labels:        `["self-hosted","linux"]`,
	}
	globalPool := db.RunnerPool{
		ID:            164,
		Name:          "global-pool",
		Provider:      "github",
		RepositoryUrl: "https://github.com",
		Scope:         "global",
		Labels:        `["self-hosted","linux"]`,
	}
	giteaPool := db.RunnerPool{
		ID:            165,
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

func TestPoolController_HandleWorkflowJob_Queued_WarmRunnerCoversDemand(t *testing.T) {
	ctx := context.Background()

	pool := db.RunnerPool{
		ID:             166,
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

	// Webhook queued event arrives -> the boot-provisioned warm runner is idle
	// on the matched target, so it covers the demand and NO runner is
	// provisioned (warm-first, docs/03 §4). The forge assigns the job to the
	// warm runner directly.
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

	if spawnCount != 1 {
		t.Fatalf("warm idle runner should cover the queued event, spawnCount = %d", spawnCount)
	}
	if controller.TotalActiveRunners() != 1 {
		t.Fatalf("expected 1 active runner, got %d", controller.TotalActiveRunners())
	}

	// A burst of further queued events provisions only the shortfall: each
	// event books demand first, so pending 2 > idle 1 spawns one, and pending
	// 3 > idle 2 spawns one more — capacity converges to demand (3 jobs, 3
	// runners) instead of the old spawn-per-event (4 runners).
	for _, jobID := range []int64{602, 603} {
		burstEvt := &webhook.WorkflowJobEvent{
			Action: "queued",
			Repository: webhook.RepositoryPayload{
				FullName: "test-org/test-repo",
				HTMLURL:  "https://github.com/test-org/test-repo",
			},
			WorkflowJob: webhook.WorkflowJobPayload{
				ID:     jobID,
				Labels: []string{"self-hosted", "linux"},
			},
		}
		if err := controller.HandleWorkflowJob(ctx, "github", burstEvt); err != nil {
			t.Fatalf("HandleWorkflowJob (burst %d) failed: %v", jobID, err)
		}
	}

	if spawnCount != 3 {
		t.Fatalf("expected burst to provision only the shortfall (3 total runners), got %d", spawnCount)
	}
	if controller.TotalActiveRunners() != 3 {
		t.Fatalf("expected 3 active runners, got %d", controller.TotalActiveRunners())
	}
}

// TestPoolController_HandleWorkflowJob_InProgress_ReplenishesIdleStandby
// verifies standby backfill through the public event surface (docs/03 §4):
// the moment a runner goes busy its min_idle slot is free, so a replacement
// is provisioned immediately — the next queued job is then covered warm
// instead of paying for a cold spawn. A leaked in_progress booking would
// make pending demand exceed the backfilled idle and flip the second queued
// event into a spawn, so the "stays 2" assertion also pins booking release.
func TestPoolController_HandleWorkflowJob_InProgress_ReplenishesIdleStandby(t *testing.T) {
	ctx := context.Background()

	pool := db.RunnerPool{
		ID:             168,
		Name:           "lifecycle-pool",
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

	var spawnedNames []string
	spawnCount := 0
	mockEngine := &orchestrator.MockContainerProvider{
		SpawnRunnerFn: func(ctx context.Context, config orchestrator.RunnerConfig) (string, error) {
			spawnCount++
			spawnedNames = append(spawnedNames, config.Name)
			return fmt.Sprintf("container-%d", spawnCount), nil
		},
		AuditRunnersFn: func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
			// Remote listing mirrors the spawned fleet (idle — both jobs have
			// completed by the time Reconcile runs), so the ghost sweep leaves
			// tracking alone and the drain path is what gets exercised.
			listing := make([]orchestrator.RunnerStatus, 0, len(spawnedNames))
			for i, name := range spawnedNames {
				listing = append(listing, orchestrator.RunnerStatus{
					PoolID:   168,
					ID:       fmt.Sprintf("container-%d", i+1),
					Name:     name,
					PoolName: "lifecycle-pool",
					State:    "running",
				})
			}
			return listing, nil
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

	event := func(action string, jobID int64, runnerName string) *webhook.WorkflowJobEvent {
		return &webhook.WorkflowJobEvent{
			Action: action,
			Repository: webhook.RepositoryPayload{
				FullName: "test-org/test-repo",
				HTMLURL:  "https://github.com/test-org/test-repo",
			},
			WorkflowJob: webhook.WorkflowJobPayload{
				ID:         jobID,
				Labels:     []string{"self-hosted", "linux"},
				RunnerName: runnerName,
			},
		}
	}

	// 801 queued: covered by the warm runner, no spawn.
	if err := controller.HandleWorkflowJob(ctx, "github", event("queued", 801, "")); err != nil {
		t.Fatalf("queued 801 failed: %v", err)
	}
	if spawnCount != 1 {
		t.Fatalf("expected warm runner to cover 801, spawnCount = %d", spawnCount)
	}

	// 801 goes in_progress on the warm runner: the standby slot it vacated
	// is backfilled immediately (spawn 2) and the booking is released.
	if err := controller.HandleWorkflowJob(ctx, "github", event("in_progress", 801, spawnedNames[0])); err != nil {
		t.Fatalf("in_progress 801 failed: %v", err)
	}
	if spawnCount != 2 {
		t.Fatalf("expected in_progress to backfill the idle standby, spawnCount = %d", spawnCount)
	}
	if controller.TotalActiveRunners() != 2 {
		t.Fatalf("expected 2 active runners after backfill, got %d", controller.TotalActiveRunners())
	}

	// 802 queued: covered by the freshly backfilled standby — no cold spawn.
	// This is the latency win: a leaked 801 booking would inflate pending
	// demand to 2, exceed the 1 idle standby, and flip this into a spawn.
	if err := controller.HandleWorkflowJob(ctx, "github", event("queued", 802, "")); err != nil {
		t.Fatalf("queued 802 failed: %v", err)
	}
	if spawnCount != 2 {
		t.Fatalf("expected 802 to be covered by the backfilled standby, spawnCount = %d", spawnCount)
	}

	// 802 goes in_progress on the backfilled standby: backfill again (3).
	if err := controller.HandleWorkflowJob(ctx, "github", event("in_progress", 802, spawnedNames[1])); err != nil {
		t.Fatalf("in_progress 802 failed: %v", err)
	}
	if spawnCount != 3 {
		t.Fatalf("expected second pickup to backfill again, spawnCount = %d", spawnCount)
	}

	// 801 completes: the warm runner frees up. 803 queued: pending is 1
	// (802 in flight plus 803 minus the released 801 = net new demand 1) vs
	// 2 idle -> covered, no spawn.
	if err := controller.HandleWorkflowJob(ctx, "github", event("completed", 801, spawnedNames[0])); err != nil {
		t.Fatalf("completed 801 failed: %v", err)
	}
	if err := controller.HandleWorkflowJob(ctx, "github", event("queued", 803, "")); err != nil {
		t.Fatalf("queued 803 failed: %v", err)
	}
	if spawnCount != 3 {
		t.Fatalf("expected 803 to be covered by freed capacity, spawnCount = %d", spawnCount)
	}

	// 802 completes: reconcile finds 3 idle against target 1 and drains the
	// excess back down — the backfill mirrors on the way out.
	if err := controller.HandleWorkflowJob(ctx, "github", event("completed", 802, spawnedNames[1])); err != nil {
		t.Fatalf("completed 802 failed: %v", err)
	}
	if err := controller.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	if controller.TotalActiveRunners() != 1 {
		t.Fatalf("expected excess idle to drain back to the standby target, got %d active", controller.TotalActiveRunners())
	}
}

// TestPoolController_HandleWorkflowJob_InProgress_ReplenishRespectsMaxConcurrency
// pins the capacity constraint on backfill: a pool at max_concurrency must
// not provision a replacement standby while a runner occupies the last slot —
// neither from the webhook hook nor from the reconcile top-up.
func TestPoolController_HandleWorkflowJob_InProgress_ReplenishRespectsMaxConcurrency(t *testing.T) {
	ctx := context.Background()

	pool := db.RunnerPool{
		ID:             170,
		Name:           "capped-backfill-pool",
		Provider:       "github",
		RepositoryUrl:  "https://github.com/test-org/test-repo",
		Scope:          "repo",
		AuthProfileID:  10,
		MinIdleRunners: 1,
		MaxConcurrency: 1,
		Labels:         `["self-hosted","linux"]`,
		RunnerImage:    "ghcr.io/noosxe/runnero:latest",
	}

	repo := &mockPoolRepo{pools: []db.RunnerPool{pool}}
	gitProv := &mockGitProvider{}
	resolver := &mockGitProviderResolver{
		providers: map[int64]provider.GitProvider{10: gitProv},
	}

	var spawnedNames []string
	spawnCount := 0
	mockEngine := &orchestrator.MockContainerProvider{
		SpawnRunnerFn: func(ctx context.Context, config orchestrator.RunnerConfig) (string, error) {
			spawnCount++
			spawnedNames = append(spawnedNames, config.Name)
			return fmt.Sprintf("container-%d", spawnCount), nil
		},
		AuditRunnersFn: func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
			// Remote listing mirrors the fleet; the boot runner is busy.
			listing := make([]orchestrator.RunnerStatus, 0, len(spawnedNames))
			for i, name := range spawnedNames {
				listing = append(listing, orchestrator.RunnerStatus{
					PoolID:   170,
					ID:       fmt.Sprintf("container-%d", i+1),
					Name:     name,
					PoolName: "capped-backfill-pool",
					State:    "running",
					IsBusy:   i == 0,
				})
			}
			return listing, nil
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

	event := func(action string, jobID int64, runnerName string) *webhook.WorkflowJobEvent {
		return &webhook.WorkflowJobEvent{
			Action: action,
			Repository: webhook.RepositoryPayload{
				FullName: "test-org/test-repo",
				HTMLURL:  "https://github.com/test-org/test-repo",
			},
			WorkflowJob: webhook.WorkflowJobPayload{
				ID:         jobID,
				Labels:     []string{"self-hosted", "linux"},
				RunnerName: runnerName,
			},
		}
	}

	// 911 queued: covered by the warm runner (no spawn); it then goes busy.
	if err := controller.HandleWorkflowJob(ctx, "github", event("queued", 911, "")); err != nil {
		t.Fatalf("queued 911 failed: %v", err)
	}
	if err := controller.HandleWorkflowJob(ctx, "github", event("in_progress", 911, spawnedNames[0])); err != nil {
		t.Fatalf("in_progress 911 failed: %v", err)
	}
	if spawnCount != 1 {
		t.Fatalf("max_concurrency=1 must block standby backfill, spawnCount = %d", spawnCount)
	}

	// The reconcile top-up is bound by the same cap: busy-inclusive target is
	// clamped to max_concurrency, so no spawn here either.
	if err := controller.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	if spawnCount != 1 {
		t.Fatalf("reconcile must respect max_concurrency during backfill, spawnCount = %d", spawnCount)
	}
	if controller.TotalActiveRunners() != 1 {
		t.Fatalf("expected the busy runner to remain the only active runner, got %d", controller.TotalActiveRunners())
	}
}

// TestPoolController_ReconcileBackfillsBusyStandbySlot verifies the
// reconcile-side half of standby backfill (docs/03 §3): a runner that went
// busy without a webhook (poll fallback, missed delivery) frees its standby
// slot all the same, and the next reconcile tick provisions the replacement.
func TestPoolController_ReconcileBackfillsBusyStandbySlot(t *testing.T) {
	ctx := context.Background()

	pool := db.RunnerPool{
		ID:             171,
		Name:           "poll-backfill-pool",
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

	var spawnedNames []string
	spawnCount := 0
	mockEngine := &orchestrator.MockContainerProvider{
		SpawnRunnerFn: func(ctx context.Context, config orchestrator.RunnerConfig) (string, error) {
			spawnCount++
			spawnedNames = append(spawnedNames, config.Name)
			return fmt.Sprintf("container-%d", spawnCount), nil
		},
		AuditRunnersFn: func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
			// Remote listing mirrors the fleet; the boot runner is busy (the
			// job was discovered by polling, no webhook delivered).
			listing := make([]orchestrator.RunnerStatus, 0, len(spawnedNames))
			for i, name := range spawnedNames {
				listing = append(listing, orchestrator.RunnerStatus{
					PoolID:   171,
					ID:       fmt.Sprintf("container-%d", i+1),
					Name:     name,
					PoolName: "poll-backfill-pool",
					State:    "running",
					IsBusy:   i == 0,
				})
			}
			return listing, nil
		},
		PingFn: func(ctx context.Context) error {
			return nil
		},
	}

	reconciler := orchestrator.NewReconciler(mockEngine)
	controller := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:               repo,
		ContainerEngine:  mockEngine,
		ProviderResolver: resolver,
		Reconciler:       reconciler,
		GlobalMaxRunners: 10,
		Interval:         time.Hour,
	})

	if err := controller.Boot(ctx); err != nil {
		t.Fatalf("boot failed: %v", err)
	}
	if spawnCount != 1 {
		t.Fatalf("expected 1 boot runner, got %d", spawnCount)
	}

	// The runner goes busy without any webhook (poll fallback discovered it).
	runners := reconciler.TrackedPoolRunners(171)
	if len(runners) != 1 {
		t.Fatalf("expected 1 tracked runner, got %d", len(runners))
	}
	reconciler.MarkRunnerBusy(runners[0].Name, true)

	if err := controller.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	if spawnCount != 2 {
		t.Fatalf("expected reconcile to backfill the standby slot of the busy runner, spawnCount = %d", spawnCount)
	}
	if active, idle := controller.PoolStats(171); active != 1 || idle != 1 {
		t.Fatalf("expected 1 busy + 1 replenished idle, got (busy=%d, idle=%d)", active, idle)
	}
}

func TestPoolController_HandleWorkflowJob_Queued_MaxConcurrencyReached(t *testing.T) {
	ctx := context.Background()

	pool := db.RunnerPool{
		ID:             167,
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

	// Two queued events are covered by the two warm idle runners (pending 1
	// then 2, idle 2) — no spawn. The third exceeds idle capacity, and only
	// then does the per-pool max_concurrency gate (2 active) reject the spawn.
	for _, jobID := range []int64{502, 503, 504} {
		evt := &webhook.WorkflowJobEvent{
			Action: "queued",
			Repository: webhook.RepositoryPayload{
				FullName: "test-org/test-repo",
				HTMLURL:  "https://github.com/test-org/test-repo",
			},
			WorkflowJob: webhook.WorkflowJobPayload{
				ID:     jobID,
				Labels: []string{"self-hosted"},
			},
		}
		if err := controller.HandleWorkflowJob(ctx, "github", evt); err != nil {
			t.Fatalf("HandleWorkflowJob (%d) failed: %v", jobID, err)
		}
	}

	if spawnCount != 2 {
		t.Fatalf("expected spawnCount to remain 2 when max concurrency is reached, got %d", spawnCount)
	}
}

func TestPoolController_HandleWorkflowJob_Queued_GlobalQuotaSaturated(t *testing.T) {
	ctx := context.Background()

	pool := db.RunnerPool{
		ID:             114,
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

	// Three queued events: the first two are covered by the two warm idle
	// runners (warm-first), the third exceeds idle capacity and only then
	// hits the saturated global quota (2 active = GlobalMaxRunners 2) -> the
	// request is enqueued internally.
	for _, jobID := range []int64{503, 504, 505} {
		evt := &webhook.WorkflowJobEvent{
			Action: "queued",
			Repository: webhook.RepositoryPayload{
				FullName: "test-org/test-repo",
				HTMLURL:  "https://github.com/test-org/test-repo",
			},
			WorkflowJob: webhook.WorkflowJobPayload{
				ID:     jobID,
				Labels: []string{"self-hosted"},
			},
		}
		if err := controller.HandleWorkflowJob(ctx, "github", evt); err != nil {
			t.Fatalf("HandleWorkflowJob (%d) failed: %v", jobID, err)
		}
	}

	if spawnCount != 2 {
		t.Fatalf("expected spawnCount to remain 2 when global quota is saturated, got %d", spawnCount)
	}
	if controller.QueueLengthForPool(114) != 1 {
		t.Fatalf("expected 1 request in internal queue, got %d", controller.QueueLengthForPool(114))
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
	if controller.QueueLengthForPool(114) != 0 {
		t.Fatalf("expected queue to be drained, remaining: %d", controller.QueueLengthForPool(114))
	}
	if spawnCount < 3 {
		t.Fatalf("expected queued runner to be spawned upon capacity release, got spawnCount=%d", spawnCount)
	}
}

func TestPoolController_HandleWorkflowJob_InProgressAndCompleted(t *testing.T) {
	ctx := context.Background()

	pool := db.RunnerPool{
		ID:             168,
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

	active, idle := controller.PoolStats(168)
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

	active, idle = controller.PoolStats(168)
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

	active, idle = controller.PoolStats(168)
	if active != 0 || idle != 1 {
		t.Fatalf("expected 0 active, 1 idle after completed; got active=%d, idle=%d", active, idle)
	}
}
