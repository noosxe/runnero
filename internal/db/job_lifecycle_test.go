package db

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// newJobLifecycleDB opens a migrated temporary database with one pool.
func newJobLifecycleDB(t *testing.T) (*DB, int64, func()) {
	t.Helper()
	dir := t.TempDir()
	database, err := Open(Options{Path: filepath.Join(dir, "lifecycle_test.db")})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	cleanup := func() { _ = database.Close() }

	ctx := context.Background()
	profile, err := database.CreateAuthProfile(ctx, CreateAuthProfileParams{
		Name:                "github-lifecycle",
		AuthMethod:          "github_app",
		AppID:               sql.NullInt64{Int64: 12345, Valid: true},
		PrivateKeyEncrypted: sql.NullString{String: "enc_priv_key", Valid: true},
		TokenEncrypted:      sql.NullString{String: "enc_token", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateAuthProfile failed: %v", err)
	}
	pool, err := database.CreateRunnerPool(ctx, CreateRunnerPoolParams{
		Name:          "lifecycle-pool",
		Provider:      "github",
		RepositoryUrl: "https://github.com/myorg/myrepo",
		Scope:         "repo",
		AuthProfileID: profile.ID,
		Labels:        `["self-hosted","linux"]`,
		RunnerImage:   "ghcr.io/noosxe/runnero:latest",
	})
	if err != nil {
		t.Fatalf("CreateRunnerPool failed: %v", err)
	}
	return database, pool.ID, cleanup
}

// TestOpenCloseTransitionJobLifecycle verifies the docs/21 §5.2 lifecycle:
// open on idle→busy (idempotent — one open row per runner slot), close on
// busy→idle with a terminal status.
func TestOpenCloseTransitionJobLifecycle(t *testing.T) {
	database, poolID, cleanup := newJobLifecycleDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC()

	if err := database.OpenTransitionJob(ctx, poolID, "runnero-a-1", now); err != nil {
		t.Fatalf("OpenTransitionJob failed: %v", err)
	}
	openID, err := database.GetOpenJobRow(ctx, GetOpenJobRowParams{PoolID: poolID, RunnerName: "runnero-a-1"})
	if err != nil {
		t.Fatalf("expected an open row after OpenTransitionJob: %v", err)
	}

	// Idempotent reopen: the one-open-row invariant (docs/21 §5.5/§5.7).
	if err := database.OpenTransitionJob(ctx, poolID, "runnero-a-1", now.Add(time.Second)); err != nil {
		t.Fatalf("idempotent OpenTransitionJob failed: %v", err)
	}
	sameID, err := database.GetOpenJobRow(ctx, GetOpenJobRowParams{PoolID: poolID, RunnerName: "runnero-a-1"})
	if err != nil || sameID != openID {
		t.Fatalf("reopen must keep the same open row: id=%d want=%d (err=%v)", sameID, openID, err)
	}

	completed := now.Add(5 * time.Minute)
	if err := database.CloseTransitionJob(ctx, poolID, "runnero-a-1", "completed", 0, "", completed); err != nil {
		t.Fatalf("CloseTransitionJob failed: %v", err)
	}

	if _, err := database.GetOpenJobRow(ctx, GetOpenJobRowParams{PoolID: poolID, RunnerName: "runnero-a-1"}); err == nil {
		t.Fatal("row must no longer be open after CloseTransitionJob")
	}
	row, err := database.GetJobHistoryById(ctx, openID)
	if err != nil {
		t.Fatalf("GetJobHistoryById failed: %v", err)
	}
	if row.Status != "completed" || row.Source != "transition" {
		t.Fatalf("unexpected closed row: status=%q source=%q", row.Status, row.Source)
	}
	if !row.CompletedAt.Valid || !row.StartedAt.Valid {
		t.Fatalf("expected started/completed timestamps, got %+v", row)
	}

	// A new job on the same runner opens a fresh row.
	if err := database.OpenTransitionJob(ctx, poolID, "runnero-a-1", completed.Add(time.Minute)); err != nil {
		t.Fatalf("reopen after close failed: %v", err)
	}
	if _, err := database.GetOpenJobRow(ctx, GetOpenJobRowParams{PoolID: poolID, RunnerName: "runnero-a-1"}); err != nil {
		t.Fatalf("expected a fresh open row: %v", err)
	}
}

// TestRecordJobTimeoutClosesOpenRow verifies the timeout path merges with the
// lifecycle row instead of duplicating it (docs/21 §5.2).
func TestRecordJobTimeoutClosesOpenRow(t *testing.T) {
	database, poolID, cleanup := newJobLifecycleDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC()

	if err := database.OpenTransitionJob(ctx, poolID, "runnero-b-1", now); err != nil {
		t.Fatalf("OpenTransitionJob failed: %v", err)
	}
	openID, _ := database.GetOpenJobRow(ctx, GetOpenJobRowParams{PoolID: poolID, RunnerName: "runnero-b-1"})

	if err := database.RecordJobTimeout(ctx, poolID, "runnero-b-1", "/logs/hung.log", now, now.Add(time.Hour)); err != nil {
		t.Fatalf("RecordJobTimeout failed: %v", err)
	}

	count, err := database.CountJobHistory(ctx)
	if err != nil || count != 1 {
		t.Fatalf("timeout must close the open row in place, count=%d (err=%v)", count, err)
	}
	row, err := database.GetJobHistoryById(ctx, openID)
	if err != nil {
		t.Fatalf("GetJobHistoryById failed: %v", err)
	}
	if row.Status != "timeout" {
		t.Fatalf("row status = %q, want timeout", row.Status)
	}
	if !row.LogRetentionPath.Valid || row.LogRetentionPath.String != "/logs/hung.log" {
		t.Fatalf("log path not retained: %+v", row.LogRetentionPath)
	}
}

// TestRecordJobTimeoutCreatesRowWithoutOpen verifies the legacy behavior for
// runners with no lifecycle row (e.g. recorded before this feature shipped).
func TestRecordJobTimeoutCreatesRowWithoutOpen(t *testing.T) {
	database, poolID, cleanup := newJobLifecycleDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC()

	if err := database.RecordJobTimeout(ctx, poolID, "runnero-c-1", "", now, now.Add(time.Hour)); err != nil {
		t.Fatalf("RecordJobTimeout failed: %v", err)
	}
	rows, err := database.ListJobHistory(ctx, ListJobHistoryParams{Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("expected exactly 1 row, got %d (err=%v)", len(rows), err)
	}
	if rows[0].Source != "timeout" {
		t.Fatalf("created row source = %q, want timeout", rows[0].Source)
	}
}

// TestCloseInterruptedOpenJobs verifies boot recovery force-closes all open
// rows as 'interrupted' (docs/21 §5.4).
func TestCloseInterruptedOpenJobs(t *testing.T) {
	database, poolID, cleanup := newJobLifecycleDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC()

	for _, name := range []string{"runnero-d-1", "runnero-d-2", "runnero-d-3"} {
		if err := database.OpenTransitionJob(ctx, poolID, name, now); err != nil {
			t.Fatalf("OpenTransitionJob(%s) failed: %v", name, err)
		}
	}

	closed, err := database.CloseInterruptedOpenJobs(ctx, now)
	if err != nil || closed != 3 {
		t.Fatalf("CloseInterruptedOpenJobs = %d (err=%v), want 3", closed, err)
	}
	rows, err := database.ListJobHistory(ctx, ListJobHistoryParams{Limit: 10})
	if err != nil {
		t.Fatalf("ListJobHistory failed: %v", err)
	}
	for _, r := range rows {
		if r.Status != "interrupted" || !r.CompletedAt.Valid {
			t.Fatalf("row not interrupted: %+v", r)
		}
	}
}

// TestCloseStaleOpenJobs verifies the belt-and-braces sweep is pool-scoped and
// cutoff-scoped (docs/21 §5.4).
func TestCloseStaleOpenJobs(t *testing.T) {
	database, poolID, cleanup := newJobLifecycleDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC()

	poolProfile, err := database.CreateAuthProfile(ctx, CreateAuthProfileParams{
		Name:                "github-lifecycle-b",
		AuthMethod:          "github_app",
		AppID:               sql.NullInt64{Int64: 12345, Valid: true},
		PrivateKeyEncrypted: sql.NullString{String: "enc_priv_key", Valid: true},
		TokenEncrypted:      sql.NullString{String: "enc_token", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateAuthProfile(b) failed: %v", err)
	}
	poolB, err := database.CreateRunnerPool(ctx, CreateRunnerPoolParams{
		Name:          "lifecycle-pool-b",
		Provider:      "github",
		RepositoryUrl: "https://github.com/myorg/other",
		Scope:         "repo",
		AuthProfileID: poolProfile.ID,
		Labels:        `["self-hosted","linux"]`,
		RunnerImage:   "ghcr.io/noosxe/runnero:latest",
	})
	if err != nil {
		t.Fatalf("CreateRunnerPool(b) failed: %v", err)
	}

	if err := database.OpenTransitionJob(ctx, poolID, "runnero-stale", now.Add(-3*time.Hour)); err != nil {
		t.Fatalf("open stale failed: %v", err)
	}
	if err := database.OpenTransitionJob(ctx, poolID, "runnero-fresh", now); err != nil {
		t.Fatalf("open fresh failed: %v", err)
	}
	if err := database.OpenTransitionJob(ctx, poolB.ID, "runnero-other-pool", now.Add(-3*time.Hour)); err != nil {
		t.Fatalf("open other-pool failed: %v", err)
	}

	closed, err := database.CloseStaleOpenJobs(ctx, poolID, now.Add(-2*time.Hour), now)
	if err != nil || closed != 1 {
		t.Fatalf("CloseStaleOpenJobs = %d (err=%v), want 1", closed, err)
	}

	stale, err := database.GetJobHistoryById(ctx, 1)
	if err == nil && stale.Status != "interrupted" {
		t.Fatalf("stale row status = %q, want interrupted", stale.Status)
	}
	fresh, err := database.GetOpenJobRow(ctx, GetOpenJobRowParams{PoolID: poolID, RunnerName: "runnero-fresh"})
	if err != nil {
		t.Fatalf("fresh row must stay open: %v", err)
	}
	_ = fresh
	if _, err := database.GetOpenJobRow(ctx, GetOpenJobRowParams{PoolID: poolB.ID, RunnerName: "runnero-other-pool"}); err != nil {
		t.Fatalf("other-pool row must stay open: %v", err)
	}
}

// TestJobStatsLifecycleSemantics verifies the docs/21 §5.6 query semantics:
// total counts all rows; the success-rate denominator only counts known
// outcomes; queue timing only counts rows with a queue-entry timestamp.
func TestJobStatsLifecycleSemantics(t *testing.T) {
	database, poolID, cleanup := newJobLifecycleDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC()

	seed := func(status string, queued, started, completed bool) {
		q := sql.NullTime{Time: now.Add(-30 * time.Minute), Valid: queued}
		s := sql.NullTime{Time: now.Add(-20 * time.Minute), Valid: started}
		c := sql.NullTime{Time: now.Add(-10 * time.Minute), Valid: completed}
		if _, err := database.CreateJobHistory(ctx, CreateJobHistoryParams{
			PoolID:      poolID,
			RunnerName:  "runnero-stats",
			Status:      status,
			QueuedAt:    q,
			StartedAt:   s,
			CompletedAt: c,
			Source:      "webhook",
		}); err != nil {
			t.Fatalf("seed %s failed: %v", status, err)
		}
	}

	seed("success", true, true, true)      // known outcome, queued, timed
	seed("completed", false, true, true)   // unknown outcome (transition-closed)
	seed("interrupted", false, true, true) // crash recovery
	seed("running", false, true, false)    // open row

	stats, err := database.GetJobStatsSince(ctx, now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("GetJobStatsSince failed: %v", err)
	}
	if stats.TotalJobs != 4 {
		t.Errorf("total = %d, want 4", stats.TotalJobs)
	}
	if got := toTestInt(stats.KnownOutcomeJobs); got != 1 {
		t.Errorf("known_outcome_jobs = %v, want 1", stats.KnownOutcomeJobs)
	}
	if got := toTestInt(stats.QueueTimedJobs); got != 1 {
		t.Errorf("queue_timed_jobs = %v, want 1", stats.QueueTimedJobs)
	}
	if got := toTestInt(stats.SuccessfulJobs); got != 1 {
		t.Errorf("successful = %v, want 1", stats.SuccessfulJobs)
	}
	if got := toTestInt(stats.FailedJobs); got != 0 {
		t.Errorf("failed = %v, want 0", stats.FailedJobs)
	}
}

func toTestInt(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	default:
		return -1
	}
}

func TestRecordWebhookQueuedUpsert(t *testing.T) {
	database, poolID, cleanup := newJobLifecycleDB(t)
	defer cleanup()
	ctx := context.Background()

	queuedAt := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	meta := WebhookJobMeta{RunID: 900, WorkflowName: "ci", HeadBranch: "main", HeadSHA: "abc123"}
	if err := database.RecordWebhookQueued(ctx, poolID, 501, meta, queuedAt); err != nil {
		t.Fatalf("RecordWebhookQueued failed: %v", err)
	}

	stub, err := database.GetOpenWebhookJobByID(ctx, sql.NullInt64{Int64: 501, Valid: true})
	if err != nil {
		t.Fatalf("queued row not found: %v", err)
	}
	if stub.RunnerName != "" || !stub.QueuedAt.Valid || !stub.QueuedAt.Time.Equal(queuedAt) {
		t.Fatalf("unexpected stub: runner=%q queued_at=%v", stub.RunnerName, stub.QueuedAt)
	}

	// Redelivery with the same metadata must not duplicate or rewrite.
	if err := database.RecordWebhookQueued(ctx, poolID, 501, meta, queuedAt.Add(time.Minute)); err != nil {
		t.Fatalf("RecordWebhookQueued redelivery failed: %v", err)
	}
	again, err := database.GetOpenWebhookJobByID(ctx, sql.NullInt64{Int64: 501, Valid: true})
	if err != nil {
		t.Fatalf("queued row vanished after redelivery: %v", err)
	}
	if again.ID != stub.ID || !again.QueuedAt.Time.Equal(queuedAt) {
		t.Fatalf("redelivery mutated the row: id=%d queued_at=%v", again.ID, again.QueuedAt)
	}
}

func TestRecordWebhookStartedMergeRules(t *testing.T) {
	ctx := context.Background()
	queuedAt := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	startedAt := queuedAt.Add(30 * time.Second)
	meta := WebhookJobMeta{RunID: 901, WorkflowName: "ci", HeadBranch: "main", HeadSHA: "def456"}

	t.Run("promote queued stub", func(t *testing.T) {
		database, poolID, cleanup := newJobLifecycleDB(t)
		defer cleanup()
		if err := database.RecordWebhookQueued(ctx, poolID, 600, meta, queuedAt); err != nil {
			t.Fatalf("queued upsert failed: %v", err)
		}
		if err := database.RecordWebhookStarted(ctx, poolID, 600, "runnero-a-1", startedAt, queuedAt, meta); err != nil {
			t.Fatalf("RecordWebhookStarted failed: %v", err)
		}
		row, err := database.GetOpenJobRow(ctx, GetOpenJobRowParams{PoolID: poolID, RunnerName: "runnero-a-1"})
		if err != nil {
			t.Fatalf("expected promoted open row: %v", err)
		}
		got, _ := database.GetJobHistoryById(ctx, row)
		if got.Status != "running" || !got.StartedAt.Time.Equal(startedAt) || !got.QueuedAt.Time.Equal(queuedAt) {
			t.Fatalf("unexpected promoted row: %+v", got)
		}
	})

	t.Run("attach to transition row", func(t *testing.T) {
		database, poolID, cleanup := newJobLifecycleDB(t)
		defer cleanup()
		if err := database.OpenTransitionJob(ctx, poolID, "runnero-b-1", startedAt); err != nil {
			t.Fatalf("OpenTransitionJob failed: %v", err)
		}
		if err := database.RecordWebhookStarted(ctx, poolID, 601, "runnero-b-1", startedAt, queuedAt, meta); err != nil {
			t.Fatalf("RecordWebhookStarted failed: %v", err)
		}
		row, _ := database.GetOpenJobRow(ctx, GetOpenJobRowParams{PoolID: poolID, RunnerName: "runnero-b-1"})
		got, _ := database.GetJobHistoryById(ctx, row)
		if !got.JobID.Valid || got.JobID.Int64 != 601 {
			t.Fatalf("transition row not enriched with job id: %+v", got)
		}
		if !got.QueuedAt.Valid || !got.QueuedAt.Time.Equal(queuedAt) {
			t.Fatalf("transition row did not adopt forge queued_at: %+v", got.QueuedAt)
		}
	})

	t.Run("absorb stub into transition row", func(t *testing.T) {
		database, poolID, cleanup := newJobLifecycleDB(t)
		defer cleanup()
		if err := database.RecordWebhookQueued(ctx, poolID, 602, meta, queuedAt); err != nil {
			t.Fatalf("queued upsert failed: %v", err)
		}
		if err := database.OpenTransitionJob(ctx, poolID, "runnero-c-1", startedAt); err != nil {
			t.Fatalf("OpenTransitionJob failed: %v", err)
		}
		if err := database.RecordWebhookStarted(ctx, poolID, 602, "runnero-c-1", startedAt, queuedAt, meta); err != nil {
			t.Fatalf("RecordWebhookStarted failed: %v", err)
		}
		// Exactly one row remains for the job: the stub was deleted and its
		// identity now lives on the runner's open transition row.
		history, err := database.ListJobHistory(ctx, ListJobHistoryParams{Limit: 10})
		if err != nil || len(history) != 1 {
			t.Fatalf("expected exactly one row after absorb, got %d (err=%v)", len(history), err)
		}
		row, _ := database.GetOpenJobRow(ctx, GetOpenJobRowParams{PoolID: poolID, RunnerName: "runnero-c-1"})
		got, _ := database.GetJobHistoryById(ctx, row)
		if !got.JobID.Valid || got.JobID.Int64 != 602 {
			t.Fatalf("transition row missing absorbed job id: %+v", got)
		}
	})

	t.Run("insert fresh running row", func(t *testing.T) {
		database, poolID, cleanup := newJobLifecycleDB(t)
		defer cleanup()
		if err := database.RecordWebhookStarted(ctx, poolID, 603, "runnero-d-1", startedAt, time.Time{}, meta); err != nil {
			t.Fatalf("RecordWebhookStarted failed: %v", err)
		}
		row, err := database.GetOpenJobRow(ctx, GetOpenJobRowParams{PoolID: poolID, RunnerName: "runnero-d-1"})
		if err != nil {
			t.Fatalf("expected fresh open row: %v", err)
		}
		got, _ := database.GetJobHistoryById(ctx, row)
		if got.Status != "running" || got.Source != "webhook" || got.QueuedAt.Valid {
			t.Fatalf("unexpected fresh row: %+v", got)
		}
	})
}

func TestRecordWebhookCompletedCloseRules(t *testing.T) {
	ctx := context.Background()
	queuedAt := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	startedAt := queuedAt.Add(30 * time.Second)
	completedAt := startedAt.Add(2 * time.Minute)
	meta := WebhookJobMeta{RunID: 902, WorkflowName: "ci", HeadBranch: "main", HeadSHA: "ghi789"}

	t.Run("close by external id removes duplicate transition row", func(t *testing.T) {
		database, poolID, cleanup := newJobLifecycleDB(t)
		defer cleanup()
		// Stub + poll-opened transition row for the same job (webhook races poll).
		if err := database.RecordWebhookQueued(ctx, poolID, 700, meta, queuedAt); err != nil {
			t.Fatalf("queued upsert failed: %v", err)
		}
		if err := database.OpenTransitionJob(ctx, poolID, "runnero-e-1", startedAt); err != nil {
			t.Fatalf("OpenTransitionJob failed: %v", err)
		}
		if err := database.RecordWebhookStarted(ctx, poolID, 700, "runnero-e-1", startedAt, queuedAt, meta); err != nil {
			t.Fatalf("RecordWebhookStarted failed: %v", err)
		}
		if err := database.RecordWebhookCompleted(ctx, poolID, 700, "runnero-e-1", "success", completedAt); err != nil {
			t.Fatalf("RecordWebhookCompleted failed: %v", err)
		}
		if _, err := database.GetOpenJobRow(ctx, GetOpenJobRowParams{PoolID: poolID, RunnerName: "runnero-e-1"}); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("expected no open row after completion: %v", err)
		}
	})

	t.Run("fallback closes transition row and enriches job id", func(t *testing.T) {
		database, poolID, cleanup := newJobLifecycleDB(t)
		defer cleanup()
		if err := database.OpenTransitionJob(ctx, poolID, "runnero-f-1", startedAt); err != nil {
			t.Fatalf("OpenTransitionJob failed: %v", err)
		}
		if err := database.RecordWebhookCompleted(ctx, poolID, 701, "runnero-f-1", "failure", completedAt); err != nil {
			t.Fatalf("RecordWebhookCompleted failed: %v", err)
		}
		history, err := database.ListJobHistory(ctx, ListJobHistoryParams{Limit: 10})
		if err != nil || len(history) != 1 {
			t.Fatalf("expected exactly one closed row, got %d (err=%v)", len(history), err)
		}
		got := history[0]
		if got.Status != "failure" || !got.JobID.Valid || got.JobID.Int64 != 701 {
			t.Fatalf("unexpected closed row: %+v", got)
		}
	})

	t.Run("no-op when nothing open", func(t *testing.T) {
		database, poolID, cleanup := newJobLifecycleDB(t)
		defer cleanup()
		if err := database.RecordWebhookCompleted(ctx, poolID, 702, "runnero-g-1", "success", completedAt); err != nil {
			t.Fatalf("RecordWebhookCompleted should be a no-op, got: %v", err)
		}
		history, _ := database.ListJobHistory(ctx, ListJobHistoryParams{Limit: 10})
		if len(history) != 0 {
			t.Fatalf("expected no rows, got %d", len(history))
		}
	})
}


// TestCloseTransitionJobEnrichesJobID verifies the docs/21 section 5.3 close
// enrichment: a non-zero job id is written onto the closing row, and a zero
// job id leaves any existing value untouched (COALESCE).
func TestCloseTransitionJobEnrichesJobID(t *testing.T) {
	database, poolID, cleanup := newJobLifecycleDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC()

	if err := database.OpenTransitionJob(ctx, poolID, "runnero-enrich-1", now); err != nil {
		t.Fatalf("OpenTransitionJob failed: %v", err)
	}
	if err := database.CloseTransitionJob(ctx, poolID, "runnero-enrich-1", "success", 9001, "", now.Add(time.Minute)); err != nil {
		t.Fatalf("CloseTransitionJob with job id failed: %v", err)
	}
	history, err := database.ListJobHistory(ctx, ListJobHistoryParams{Limit: 10})
	if err != nil {
		t.Fatalf("ListJobHistory failed: %v", err)
	}
	if len(history) != 1 || !history[0].JobID.Valid || history[0].JobID.Int64 != 9001 {
		t.Fatalf("expected one row enriched with job id 9001, got %+v", history)
	}

	// A zero job id must not clobber a previously stored value.
	if err := database.RecordWebhookQueued(ctx, poolID, 9002, WebhookJobMeta{}, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("RecordWebhookQueued failed: %v", err)
	}
	if err := database.RecordWebhookStarted(ctx, poolID, 9002, "runnero-enrich-1", now.Add(2*time.Minute), now.Add(2*time.Minute), WebhookJobMeta{}); err != nil {
		t.Fatalf("RecordWebhookStarted failed: %v", err)
	}
	if err := database.CloseTransitionJob(ctx, poolID, "runnero-enrich-1", "completed", 0, "", now.Add(3*time.Minute)); err != nil {
		t.Fatalf("CloseTransitionJob without job id failed: %v", err)
	}
	history, err = database.ListJobHistory(ctx, ListJobHistoryParams{Limit: 10})
	if err != nil {
		t.Fatalf("ListJobHistory failed: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("expected 2 closed rows, got %d: %+v", len(history), history)
	}
	for _, r := range history {
		if !r.JobID.Valid {
			t.Fatalf("every closed row must carry a job id, got %+v", r)
		}
	}
}
