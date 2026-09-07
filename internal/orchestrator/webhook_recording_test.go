package orchestrator_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/noosxe/runnero/internal/db"
	"github.com/noosxe/runnero/internal/orchestrator"
	"github.com/noosxe/runnero/internal/provider"
	"github.com/noosxe/runnero/internal/webhook"
)

// newWebhookRecHarness builds a booted controller with a job-history recorder
// mock and one tracked idle runner, ready to receive workflow_job events.
func newWebhookRecHarness(t *testing.T, rec *mockJobRecorder) (*orchestrator.PoolController, *orchestrator.Reconciler) {
	t.Helper()

	pool := db.RunnerPool{
		ID:             1,
		Name:           "enrich-pool",
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
	engine := &orchestrator.MockContainerProvider{
		SpawnRunnerFn: func(ctx context.Context, config orchestrator.RunnerConfig) (string, error) {
			return "container-" + config.Name, nil
		},
		AuditRunnersFn: func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
			return nil, nil
		},
		PingFn: func(ctx context.Context) error { return nil },
	}
	reconciler := orchestrator.NewReconciler(engine)

	ctrl := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:               repo,
		ContainerEngine:  engine,
		ProviderResolver: resolver,
		Reconciler:       reconciler,
		JobRecorder:      rec,
		Interval:         time.Hour,
	})
	if err := ctrl.Boot(context.Background()); err != nil {
		t.Fatalf("boot failed: %v", err)
	}
	return ctrl, reconciler
}

// trackRunner registers a tracked runner directly with the reconciler.
func trackRunner(reconciler *orchestrator.Reconciler, name string, busy bool) {
	reconciler.TrackRunner(orchestrator.RunnerStatus{
		ID:        "c-" + name,
		Name:      name,
		PoolName:  "enrich-pool",
		State:     "running",
		IsBusy:    busy,
		SpawnedAt: time.Now().UTC(),
	})
}

func trackedBusy(reconciler *orchestrator.Reconciler, name string) bool {
	for _, r := range reconciler.TrackedPoolRunners("enrich-pool") {
		if r.Name == name {
			return r.IsBusy
		}
	}
	return false
}

func enrichedEvent(action string, jobID int64, runnerName, conclusion string) *webhook.WorkflowJobEvent {
	return &webhook.WorkflowJobEvent{
		Action: action,
		Repository: webhook.RepositoryPayload{
			FullName: "test-org/test-repo",
			HTMLURL:  "https://github.com/test-org/test-repo",
		},
		WorkflowJob: webhook.WorkflowJobPayload{
			ID:           jobID,
			RunID:        909,
			WorkflowName: "ci",
			HeadBranch:   "main",
			HeadSHA:      "abc123",
			Conclusion:   conclusion,
			Labels:       []string{"self-hosted", "linux"},
			RunnerName:   runnerName,
			CreatedAt:    "2026-09-07T12:00:00Z",
			StartedAt:    "2026-09-07T12:00:30Z",
			CompletedAt:  "2026-09-07T12:02:30Z",
		},
	}
}

func TestWebhookEnrichment_QueuedEventUpsertsRow(t *testing.T) {
	ctx := context.Background()
	rec := &mockJobRecorder{}
	ctrl, _ := newWebhookRecHarness(t, rec)

	if err := ctrl.HandleWorkflowJob(ctx, "github", enrichedEvent("queued", 501, "", "")); err != nil {
		t.Fatalf("HandleWorkflowJob failed: %v", err)
	}

	if len(rec.webhookQueued) != 1 {
		t.Fatalf("expected 1 queued upsert, got %d", len(rec.webhookQueued))
	}
	call := rec.webhookQueued[0]
	if call.poolID != 1 || call.jobID != 501 {
		t.Fatalf("unexpected call: pool=%d job=%d", call.poolID, call.jobID)
	}
	wantQueued := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	if !call.queuedAt.Equal(wantQueued) {
		t.Fatalf("queued_at = %v, want %v", call.queuedAt, wantQueued)
	}
	if call.meta.RunID != 909 || call.meta.WorkflowName != "ci" || call.meta.HeadSHA != "abc123" {
		t.Fatalf("unexpected meta: %+v", call.meta)
	}
}

func TestWebhookEnrichment_InProgressMarksBusyAndRecords(t *testing.T) {
	ctx := context.Background()
	rec := &mockJobRecorder{}
	ctrl, reconciler := newWebhookRecHarness(t, rec)
	trackRunner(reconciler, "runnero-enrich-1", false)

	if err := ctrl.HandleWorkflowJob(ctx, "github", enrichedEvent("in_progress", 502, "runnero-enrich-1", "")); err != nil {
		t.Fatalf("HandleWorkflowJob failed: %v", err)
	}

	if !trackedBusy(reconciler, "runnero-enrich-1") {
		t.Fatalf("runner should be marked busy")
	}
	if len(rec.webhookStarted) != 1 {
		t.Fatalf("expected 1 started record, got %d", len(rec.webhookStarted))
	}
	call := rec.webhookStarted[0]
	if call.runnerName != "runnero-enrich-1" || call.jobID != 502 {
		t.Fatalf("unexpected call: %+v", call)
	}
	wantStarted := time.Date(2026, 9, 7, 12, 0, 30, 0, time.UTC)
	wantQueued := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	if !call.startedAt.Equal(wantStarted) || !call.queuedAt.Equal(wantQueued) {
		t.Fatalf("timestamps: started=%v queued=%v", call.startedAt, call.queuedAt)
	}
}

func TestWebhookEnrichment_CompletedUnmarksBusyAndCloses(t *testing.T) {
	ctx := context.Background()
	rec := &mockJobRecorder{}
	ctrl, reconciler := newWebhookRecHarness(t, rec)
	trackRunner(reconciler, "runnero-enrich-2", true)

	if err := ctrl.HandleWorkflowJob(ctx, "github", enrichedEvent("completed", 503, "runnero-enrich-2", "failure")); err != nil {
		t.Fatalf("HandleWorkflowJob failed: %v", err)
	}

	if trackedBusy(reconciler, "runnero-enrich-2") {
		t.Fatalf("runner should have been unmarked busy")
	}
	if len(rec.webhookCompleted) != 1 {
		t.Fatalf("expected 1 completed record, got %d", len(rec.webhookCompleted))
	}
	call := rec.webhookCompleted[0]
	if call.status != "failure" {
		t.Fatalf("status = %q, want failure", call.status)
	}
	wantCompleted := time.Date(2026, 9, 7, 12, 2, 30, 0, time.UTC)
	if !call.completedAt.Equal(wantCompleted) {
		t.Fatalf("completed_at = %v, want %v", call.completedAt, wantCompleted)
	}
}

func TestWebhookEnrichment_RecorderFailureFailsOpen(t *testing.T) {
	ctx := context.Background()
	rec := &mockJobRecorder{failWebhook: errors.New("db down")}
	ctrl, reconciler := newWebhookRecHarness(t, rec)
	trackRunner(reconciler, "runnero-enrich-3", false)

	// Recording failures must never fail the webhook handling (docs/21 G3),
	// and the fast-path busy flags must keep flipping.
	fire := func(action string) {
		evt := enrichedEvent(action, 504, "runnero-enrich-3", "success")
		if err := ctrl.HandleWorkflowJob(ctx, "github", evt); err != nil {
			t.Fatalf("HandleWorkflowJob(%s) failed: %v", action, err)
		}
	}

	fire("in_progress")
	if !trackedBusy(reconciler, "runnero-enrich-3") {
		t.Fatalf("in_progress fast-path busy flag must still apply when recording fails")
	}
	fire("completed")
	if trackedBusy(reconciler, "runnero-enrich-3") {
		t.Fatalf("completed fast-path unbusy flag must still apply when recording fails")
	}
	fire("queued")
}

func TestWebhookEnrichment_NoMatchingPoolSkipsRecording(t *testing.T) {
	ctx := context.Background()
	rec := &mockJobRecorder{}
	ctrl, _ := newWebhookRecHarness(t, rec)

	evt := enrichedEvent("queued", 505, "", "")
	evt.Repository = webhook.RepositoryPayload{
		FullName: "other-org/other-repo",
		HTMLURL:  "https://github.com/other-org/other-repo",
	}
	if err := ctrl.HandleWorkflowJob(ctx, "github", evt); err != nil {
		t.Fatalf("HandleWorkflowJob failed: %v", err)
	}
	if len(rec.webhookQueued) != 0 {
		t.Fatalf("expected no recording for unmatched pool, got %d", len(rec.webhookQueued))
	}
}
