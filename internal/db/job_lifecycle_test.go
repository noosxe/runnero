package db

import (
	"context"
	"database/sql"
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
	if err := database.CloseTransitionJob(ctx, poolID, "runnero-a-1", "completed", "", completed); err != nil {
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
