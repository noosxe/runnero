package server

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/noosxe/runnero/internal/db"
)

// RunnerLogResolver resolves a runner name to the runner id its historical
// capture is filed under (RUN-252). Captures are written to
// DATA_DIR/logs/<container-id>.log.jsonl.gz (orchestrator.CaptureLogs keys on
// the container ID), while callers — the history page and the /logs Runners
// tab — key runners by container name. The resolver bridges that gap through
// job_history.log_retention_path, which stores the exact capture path at job
// close. Kept as a seam so LogService handlers stay mock-based in tests
// (docs/29 §5.2).
type RunnerLogResolver interface {
	// LatestCaptureRunnerID returns the container id of the most recent
	// capture recorded for runnerName, or an error (sql.ErrNoRows) when no
	// capture exists. Returned ids are validated: the only thing callers may
	// do with them is rebuild a path under DATA_DIR/logs.
	LatestCaptureRunnerID(ctx context.Context, runnerName string) (string, error)
}

// captureFileSuffix is the file-name suffix orchestrator.CaptureAndCompressLogs
// writes captures under: <container-id>.log.jsonl.gz.
const captureFileSuffix = ".log.jsonl.gz"

// dbRunnerLogResolver adapts *db.DB to RunnerLogResolver.
type dbRunnerLogResolver struct {
	db *db.DB
}

// NewDBRunnerLogResolver returns the job-history-backed RunnerLogResolver.
func NewDBRunnerLogResolver(database *db.DB) RunnerLogResolver {
	return dbRunnerLogResolver{db: database}
}

// LatestCaptureRunnerID implements RunnerLogResolver. The stored path is
// never trusted as a path: it is reduced to its base name, the capture
// suffix is stripped, and the remaining id is re-validated with the same
// rule that guards every client-supplied log name (safeLogResourceName), so
// a stale or tampered row can only ever re-point GetRunnerLogs at another
// file inside DATA_DIR/logs (docs/29 §5.2).
func (r dbRunnerLogResolver) LatestCaptureRunnerID(ctx context.Context, runnerName string) (string, error) {
	if _, err := safeLogResourceName(runnerName); err != nil {
		return "", fmt.Errorf("invalid runner name %q: %w", runnerName, err)
	}
	stored, err := r.db.GetLatestJobRetentionPathByRunnerName(ctx, runnerName)
	if err != nil {
		return "", err
	}
	if !stored.Valid || stored.String == "" {
		return "", sql.ErrNoRows
	}
	id := strings.TrimSuffix(filepath.Base(stored.String), captureFileSuffix)
	if id == "" || id == filepath.Base(stored.String) {
		return "", fmt.Errorf("malformed capture path %q in job history", stored.String)
	}
	if _, err := safeLogResourceName(id); err != nil {
		return "", fmt.Errorf("invalid capture id %q resolved from job history: %w", id, err)
	}
	return id, nil
}
