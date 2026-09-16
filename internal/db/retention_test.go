package db

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRetentionPruningAndFileDeletion verifies RUN-18 acceptance criteria:
// - job_history rows older than job_retention_days are deleted
// - log files at log_retention_path are deleted alongside their rows
// - recent jobs and their log files are preserved
func TestRetentionPruningAndFileDeletion(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "retention.db")
	logsDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logsDir, 0o750); err != nil {
		t.Fatalf("creating logs dir: %v", err)
	}

	database, err := Open(Options{
		Path:          dbPath,
		EncryptionKey: bytes.Repeat([]byte{0x33}, 32),
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = database.Close() }()

	ctx := context.Background()

	// Ensure job_retention_days = 30
	if _, err := database.SetAppSetting(ctx, SetAppSettingParams{
		Key:   "job_retention_days",
		Value: "30",
	}); err != nil {
		t.Fatalf("SetAppSetting failed: %v", err)
	}

	prof, err := database.CreateEncryptedAuthProfile(ctx, "ret-prof", "pat", sql.NullInt64{}, "", "tok")
	if err != nil {
		t.Fatalf("CreateEncryptedAuthProfile: %v", err)
	}

	// Create test runner pool
	pool, err := database.CreateRunnerPool(ctx, CreateRunnerPoolParams{
		Name:          "retention-pool",
		Provider:      "github",
		RepositoryUrl: "https://github.com/org/repo",
		Scope:         "repo",
		RunnerImage:   "img",
		AuthProfileID: prof.ID,
	})
	if err != nil {
		t.Fatalf("CreateRunnerPool: %v", err)
	}

	// 1. Old job (completed 45 days ago) with physical log file
	oldLogFile := filepath.Join(logsDir, "runner-old-123.log.jsonl.gz")
	if err := os.WriteFile(oldLogFile, []byte("compressed log data for old job"), 0o644); err != nil {
		t.Fatalf("writing old log file: %v", err)
	}

	completedOld := time.Now().UTC().AddDate(0, 0, -45)
	oldJob, err := database.CreateJobHistory(ctx, CreateJobHistoryParams{
		PoolID:           pool.ID,
		RunnerName:       "runner-old-1",
		Status:           "success",
		QueuedAt:         sql.NullTime{Time: completedOld.Add(-10 * time.Minute), Valid: true},
		StartedAt:        sql.NullTime{Time: completedOld.Add(-5 * time.Minute), Valid: true},
		CompletedAt:      sql.NullTime{Time: completedOld, Valid: true},
		LogRetentionPath: sql.NullString{String: oldLogFile, Valid: true},
		Source:           "webhook",
	})
	if err != nil {
		t.Fatalf("CreateJobHistory (old): %v", err)
	}

	// 2. Recent job (completed 2 days ago) with physical log file
	recentLogFile := filepath.Join(logsDir, "runner-recent-456.log.jsonl.gz")
	if err := os.WriteFile(recentLogFile, []byte("compressed log data for recent job"), 0o644); err != nil {
		t.Fatalf("writing recent log file: %v", err)
	}

	completedRecent := time.Now().UTC().AddDate(0, 0, -2)
	recentJob, err := database.CreateJobHistory(ctx, CreateJobHistoryParams{
		PoolID:           pool.ID,
		RunnerName:       "runner-recent-2",
		Status:           "success",
		QueuedAt:         sql.NullTime{Time: completedRecent.Add(-10 * time.Minute), Valid: true},
		StartedAt:        sql.NullTime{Time: completedRecent.Add(-5 * time.Minute), Valid: true},
		CompletedAt:      sql.NullTime{Time: completedRecent, Valid: true},
		LogRetentionPath: sql.NullString{String: recentLogFile, Valid: true},
		Source:           "webhook",
	})
	if err != nil {
		t.Fatalf("CreateJobHistory (recent): %v", err)
	}

	// Run retention pruning (activeRunners = 0, no load-skip)
	res, err := database.PruneJobHistory(ctx, 0)
	if err != nil {
		t.Fatalf("PruneJobHistory failed: %v", err)
	}

	if res.Skipped {
		t.Fatalf("expected pruning to run, but was skipped: %s", res.Reason)
	}
	if res.RowsDeleted != 1 {
		t.Errorf("expected 1 row deleted, got %d", res.RowsDeleted)
	}
	if res.FilesDeleted != 1 {
		t.Errorf("expected 1 file deleted, got %d", res.FilesDeleted)
	}

	// Verify old log file is gone from disk
	if _, err := os.Stat(oldLogFile); !os.IsNotExist(err) {
		t.Errorf("expected old log file %s to be deleted, but it still exists", oldLogFile)
	}

	// Verify recent log file still exists on disk
	if _, err := os.Stat(recentLogFile); err != nil {
		t.Errorf("expected recent log file %s to exist, but got error: %v", recentLogFile, err)
	}

	// Verify DB state: old job deleted, recent job intact
	if _, err := database.GetJobHistoryById(ctx, oldJob.ID); err == nil {
		t.Errorf("expected old job %d to be deleted from database", oldJob.ID)
	}
	if _, err := database.GetJobHistoryById(ctx, recentJob.ID); err != nil {
		t.Errorf("expected recent job %d to remain in database, got error: %v", recentJob.ID, err)
	}
}

// TestRetentionPruningLoadSkip verifies that pruning is skipped when active runner count
// exceeds 80% of total_allowed_runners per OQ #4.
func TestRetentionPruningLoadSkip(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "loadskip.db")

	database, err := Open(Options{
		Path:          dbPath,
		EncryptionKey: bytes.Repeat([]byte{0x44}, 32),
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = database.Close() }()

	ctx := context.Background()

	// total_allowed_runners = 20 (80% is 16)
	if _, err := database.SetAppSetting(ctx, SetAppSettingParams{
		Key:   "total_allowed_runners",
		Value: "20",
	}); err != nil {
		t.Fatalf("SetAppSetting failed: %v", err)
	}

	// Case 1: High load (17 active runners = 85% > 80%) -> Should skip!
	resSkip, err := database.PruneJobHistory(ctx, 17)
	if err != nil {
		t.Fatalf("PruneJobHistory failed: %v", err)
	}
	if !resSkip.Skipped {
		t.Fatal("expected pruning to be skipped when active runners = 17 (85%), but it ran")
	}
	if resSkip.RowsDeleted != 0 || resSkip.FilesDeleted != 0 {
		t.Errorf("expected 0 deletions on skip, got rows=%d files=%d", resSkip.RowsDeleted, resSkip.FilesDeleted)
	}

	// Case 2: Acceptable load (15 active runners = 75% < 80%) -> Should run!
	resRun, err := database.PruneJobHistory(ctx, 15)
	if err != nil {
		t.Fatalf("PruneJobHistory failed: %v", err)
	}
	if resRun.Skipped {
		t.Fatalf("expected pruning to proceed when active runners = 15 (75%%), but was skipped: %s", resRun.Reason)
	}
}

// TestNextMidnightUTC verifies that the duration until midnight UTC is correctly calculated.
func TestNextMidnightUTC(t *testing.T) {
	// Test at 23:30 UTC -> next midnight should be ~30 minutes away
	refTime := time.Date(2026, 9, 3, 23, 30, 0, 0, time.UTC)
	delay := NextMidnightUTC(refTime)
	want := 30 * time.Minute
	if delay != want {
		t.Errorf("NextMidnightUTC at 23:30 = %v, want %v", delay, want)
	}

	// Test at 00:00 UTC -> next midnight is 24 hours away
	refMidnight := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	delayMidnight := NextMidnightUTC(refMidnight)
	want24h := 24 * time.Hour
	if delayMidnight != want24h {
		t.Errorf("NextMidnightUTC at 00:00 = %v, want %v", delayMidnight, want24h)
	}

	// For current time, delay must always be in range (0, 24h]
	nowDelay := NextMidnightUTC(time.Now())
	if nowDelay <= 0 || nowDelay > 24*time.Hour {
		t.Errorf("NextMidnightUTC(time.Now()) = %v, must be in (0, 24h]", nowDelay)
	}
}

// TestRetentionSchedulerStartAndCancel verifies that the scheduler starts and terminates on cancel.
func TestRetentionSchedulerStartAndCancel(t *testing.T) {
	dir := t.TempDir()
	database, err := Open(Options{
		Path:          filepath.Join(dir, "sched.db"),
		EncryptionKey: bytes.Repeat([]byte{0x55}, 32),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = database.Close() }()

	rs := NewRetentionScheduler(database, func(ctx context.Context) (int, error) {
		return 0, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		rs.Start(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
		// Clean exit
	case <-time.After(2 * time.Second):
		t.Fatal("RetentionScheduler.Start did not stop after context cancel")
	}
}

// TestAuthMaintenance verifies the docs/32 section 5.2 sweep: sessions past
// either expiry clock are purged while live ones survive, the audit horizon
// arithmetic purges exactly the rows older than the cutoff, and the sweep
// is idempotent. audit_logs.created_at is DEFAULT CURRENT_TIMESTAMP, so the
// audit horizon is exercised from both sides of "now": a huge retention
// (nothing is old enough to purge) and a ~zero retention (everything is).
func TestAuthMaintenance(t *testing.T) {
	ctx := context.Background()
	database, err := Open(Options{
		Path:          filepath.Join(t.TempDir(), "auth-maintenance.db"),
		EncryptionKey: bytes.Repeat([]byte{0x55}, 32),
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = database.Close() }()

	admin, err := database.CreateAdminUser(ctx, CreateAdminUserParams{Username: "admin", PasswordHash: "x"})
	if err != nil {
		t.Fatalf("CreateAdminUser: %v", err)
	}

	now := time.Now()
	sessions := []CreateSessionParams{
		// Live on both clocks: survives.
		{UserID: admin.ID, TokenHash: "live", ExpiresAt: now.Add(time.Hour), AbsoluteExpiresAt: now.Add(24 * time.Hour)},
		// Idle deadline passed: purged.
		{UserID: admin.ID, TokenHash: "idle-dead", ExpiresAt: now.Add(-time.Minute), AbsoluteExpiresAt: now.Add(24 * time.Hour)},
		// Absolute cap passed even though the idle deadline has not: purged.
		{UserID: admin.ID, TokenHash: "cap-dead", ExpiresAt: now.Add(time.Hour), AbsoluteExpiresAt: now.Add(-time.Minute)},
	}
	for _, sp := range sessions {
		if _, err := database.CreateSession(ctx, sp); err != nil {
			t.Fatalf("CreateSession %s: %v", sp.TokenHash, err)
		}
	}

	for _, action := range []string{"auth.login_success", "pool.create"} {
		if _, err := database.CreateAuditLog(ctx, CreateAuditLogParams{
			UserID: sql.NullInt64{Int64: admin.ID, Valid: true},
			Action: action,
		}); err != nil {
			t.Fatalf("CreateAuditLog %s: %v", action, err)
		}
	}

	// Huge retention: the fresh audit rows are inside the horizon.
	res, err := database.AuthMaintenance(ctx, 90*24*time.Hour)
	if err != nil {
		t.Fatalf("AuthMaintenance: %v", err)
	}
	if res.SessionsPurged != 2 {
		t.Errorf("sessions purged = %d, want 2", res.SessionsPurged)
	}
	if res.AuditsPurged != 0 {
		t.Errorf("audits purged = %d, want 0 (rows are fresh)", res.AuditsPurged)
	}

	if _, err := database.GetSessionByTokenHash(ctx, "live"); err != nil {
		t.Errorf("live session should survive the sweep: %v", err)
	}
	for _, hash := range []string{"idle-dead", "cap-dead"} {
		if _, err := database.GetSessionByTokenHash(ctx, hash); err != sql.ErrNoRows {
			t.Errorf("session %q should be purged, got: %v", hash, err)
		}
	}

	// ~Zero retention: every audit row is now beyond the horizon.
	res2, err := database.AuthMaintenance(ctx, time.Nanosecond)
	if err != nil {
		t.Fatalf("second AuthMaintenance: %v", err)
	}
	if res2.SessionsPurged != 0 {
		t.Errorf("second sweep sessions purged = %d, want 0 (none left)", res2.SessionsPurged)
	}
	if res2.AuditsPurged != 2 {
		t.Errorf("audits purged = %d, want 2", res2.AuditsPurged)
	}

	// Idempotent: a third sweep finds nothing.
	res3, err := database.AuthMaintenance(ctx, time.Nanosecond)
	if err != nil {
		t.Fatalf("third AuthMaintenance: %v", err)
	}
	if res3.SessionsPurged != 0 || res3.AuditsPurged != 0 {
		t.Errorf("third sweep purged %+v, want zeros", res3)
	}
}
