package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"time"
)

const (
	DefaultJobRetentionDays    = 30
	DefaultTotalAllowedRunners = 20
	// LoadSkipThresholdPercent defines the capacity percentage above which
	// daily retention pruning is skipped per OQ #4 (active runners > 80% of total allowed).
	LoadSkipThresholdPercent = 80
)

// PruneResult summarizes the outcome of a retention pruning operation.
type PruneResult struct {
	Skipped      bool   `json:"skipped"`
	Reason       string `json:"reason,omitempty"`
	RowsDeleted  int    `json:"rows_deleted"`
	FilesDeleted int    `json:"files_deleted"`
}

// ActiveRunnerFunc is a callback that returns the current number of active runner containers.
type ActiveRunnerFunc func(ctx context.Context) (int, error)

// RetentionScheduler manages the daily midnight UTC background cron that purges
// expired job_history rows and associated log files on disk (docs/01 §2.2, OQ #4).
type RetentionScheduler struct {
	db             *DB
	activeRunnerFn ActiveRunnerFunc
}

// NewRetentionScheduler creates a new RetentionScheduler instance.
func NewRetentionScheduler(db *DB, activeRunnerFn ActiveRunnerFunc) *RetentionScheduler {
	return &RetentionScheduler{
		db:             db,
		activeRunnerFn: activeRunnerFn,
	}
}

// PruneJobHistory prunes job_history records and associated log files older than job_retention_days.
// It skips execution if activeRunners exceeds 80% of total_allowed_runners per OQ #4.
func (d *DB) PruneJobHistory(ctx context.Context, activeRunners int) (*PruneResult, error) {
	totalAllowed := DefaultTotalAllowedRunners
	if setting, err := d.GetAppSetting(ctx, "total_allowed_runners"); err == nil {
		if val, err := strconv.Atoi(setting.Value); err == nil && val > 0 {
			totalAllowed = val
		}
	}

	retentionDays := DefaultJobRetentionDays
	if setting, err := d.GetAppSetting(ctx, "job_retention_days"); err == nil {
		if val, err := strconv.Atoi(setting.Value); err == nil && val > 0 {
			retentionDays = val
		}
	}

	// Load-skip condition: skip if active runner count > 80% of total allowed runners
	// (activeRunners * 100 > totalAllowed * 80)
	if activeRunners*100 > totalAllowed*LoadSkipThresholdPercent {
		reason := fmt.Sprintf("high load: active runners (%d/%d, %.1f%%) exceeds %d%% threshold",
			activeRunners, totalAllowed, float64(activeRunners)/float64(totalAllowed)*100, LoadSkipThresholdPercent)
		logger.Info("retention pruning skipped due to high load",
			"active_runners", activeRunners,
			"total_allowed", totalAllowed,
			"threshold_percent", LoadSkipThresholdPercent,
		)
		return &PruneResult{
			Skipped: true,
			Reason:  reason,
		}, nil
	}

	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays)

	rows, err := d.PruneJobHistoryOlderThan(ctx, PruneJobHistoryOlderThanParams{
		CompletedAt: sql.NullTime{Time: cutoff, Valid: true},
		CreatedAt:   cutoff,
	})
	if err != nil {
		return nil, fmt.Errorf("pruning job_history rows: %w", err)
	}

	filesDeleted := 0
	for _, row := range rows {
		if row.LogRetentionPath.Valid && row.LogRetentionPath.String != "" {
			path := row.LogRetentionPath.String
			if fi, err := os.Stat(path); err == nil && !fi.IsDir() {
				if err := os.Remove(path); err == nil {
					filesDeleted++
					logger.Debug("deleted pruned log file", "path", path, "job_id", row.ID)
				} else {
					logger.Error("failed to delete log file for pruned job", "path", path, "job_id", row.ID, "err", err)
				}
			}
		}
	}

	logger.Info("retention pruning completed",
		"rows_deleted", len(rows),
		"files_deleted", filesDeleted,
		"retention_days", retentionDays,
		"cutoff", cutoff,
	)

	return &PruneResult{
		Skipped:      false,
		RowsDeleted:  len(rows),
		FilesDeleted: filesDeleted,
	}, nil
}

// AuthMaintenanceResult summarizes one auth-maintenance sweep (docs/32 section 5.2).
type AuthMaintenanceResult struct {
	SessionsPurged int64 `json:"sessions_purged"`
	AuditsPurged   int64 `json:"audits_purged"`
}

// AuthMaintenance purges sessions past either expiry clock and audit rows
// older than the retention horizon (docs/32 section 5.2). Idempotent and
// counted; called hourly from the supervisor daemon.
func (d *DB) AuthMaintenance(ctx context.Context, auditRetention time.Duration) (*AuthMaintenanceResult, error) {
	res := &AuthMaintenanceResult{}

	sessionsPurged, err := d.PurgeExpiredSessions(ctx, time.Now())
	if err != nil {
		return nil, fmt.Errorf("purging expired sessions: %w", err)
	}
	res.SessionsPurged = sessionsPurged

	if auditRetention > 0 {
		auditsPurged, err := d.PurgeAuditLogsOlderThan(ctx, time.Now().Add(-auditRetention))
		if err != nil {
			return nil, fmt.Errorf("purging audit logs: %w", err)
		}
		res.AuditsPurged = auditsPurged
	}

	return res, nil
}

// Prune executes a single retention pruning cycle immediately.
func (rs *RetentionScheduler) Prune(ctx context.Context) (*PruneResult, error) {
	activeRunners := 0
	if rs.activeRunnerFn != nil {
		count, err := rs.activeRunnerFn(ctx)
		if err != nil {
			logger.Error("failed to get active runner count for retention pruning check", "err", err)
		} else {
			activeRunners = count
		}
	}
	return rs.db.PruneJobHistory(ctx, activeRunners)
}

// NextMidnightUTC returns the duration between now and the next midnight (00:00:00 UTC).
func NextMidnightUTC(now time.Time) time.Duration {
	nowUTC := now.UTC()
	nextMidnight := time.Date(nowUTC.Year(), nowUTC.Month(), nowUTC.Day()+1, 0, 0, 0, 0, time.UTC)
	return nextMidnight.Sub(nowUTC)
}

// Start launches the background ticker that fires every day at midnight UTC.
func (rs *RetentionScheduler) Start(ctx context.Context) {
	logger.Info("retention pruning scheduler started (daily at midnight UTC)")

	for {
		delay := NextMidnightUTC(time.Now())
		timer := time.NewTimer(delay)

		select {
		case <-ctx.Done():
			timer.Stop()
			logger.Info("retention pruning scheduler stopped")
			return
		case <-timer.C:
			pruneCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			res, err := rs.Prune(pruneCtx)
			cancel()
			if err != nil {
				logger.Error("daily retention pruning failed", "err", err)
			} else if res.Skipped {
				logger.Info("daily retention pruning skipped", "reason", res.Reason)
			} else {
				logger.Info("daily retention pruning finished", "rows_deleted", res.RowsDeleted, "files_deleted", res.FilesDeleted)
			}
		}
	}
}
