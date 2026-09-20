package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/noosxe/runnero/internal/db"
)

// RunnerLogResolver resolves a runner name to the runner id its historical
// capture is filed under (RUN-252). Captures are written to
// DATA_DIR/logs/<container-id>.log.jsonl.gz (orchestrator.CaptureLogs keys on
// the container ID), while callers — the history page and the /logs Runners
// tab — key runners by container name. Resolution has two stages:
//
//  1. job_history.log_retention_path — the exact capture path recorded when
//     the job row closed (close-time capture on the busy->idle flip, removal
//     capture on death/timeout).
//  2. removal records (logs/removals.jsonl) — the latest removal for the
//     name with a successful capture; this retroactively serves jobs whose
//     rows predate close-time path recording, and covers rows closed on the
//     echo-suppressed reap branch.
//
// Kept as a seam so LogService handlers stay mock-based in tests
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

// dbRunnerLogResolver adapts *db.DB (stage 1) plus the removals journal
// under dataDir (stage 2) to RunnerLogResolver.
type dbRunnerLogResolver struct {
	db      *db.DB
	dataDir string
}

// NewDBRunnerLogResolver returns the job-history- and removal-backed
// RunnerLogResolver. dataDir may be empty, which disables stage 2.
func NewDBRunnerLogResolver(database *db.DB, dataDir string) RunnerLogResolver {
	return dbRunnerLogResolver{db: database, dataDir: dataDir}
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
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if err == nil && stored.Valid && stored.String != "" {
		return captureIDFromPath(stored.String)
	}
	// Stage 2: no usable job-history path — consult the removals journal.
	return r.latestRemovalCaptureID(runnerName)
}

// captureIDFromPath reduces a stored capture path to its validated runner id:
// base name, capture suffix stripped, same resource-name rule as every
// client-supplied log name. A stale or tampered path can therefore only ever
// re-point GetRunnerLogs at another file inside DATA_DIR/logs — never escape
// it (docs/29 §5.2).
func captureIDFromPath(path string) (string, error) {
	id := strings.TrimSuffix(filepath.Base(path), captureFileSuffix)
	if id == "" || id == filepath.Base(path) {
		return "", fmt.Errorf("malformed capture path %q", path)
	}
	if _, err := safeLogResourceName(id); err != nil {
		return "", fmt.Errorf("invalid capture id %q resolved from %q: %w", id, path, err)
	}
	return id, nil
}

// latestRemovalCaptureID scans the tail of logs/removals.jsonl for the most
// recent removal of runnerName whose capture succeeded, returning its runner
// id. Reuses the ListRemovalRecords bounded tail-read (removalScanCap lines),
// so a pathological journal cannot turn one log request into unbounded IO.
// Missing or unreadable journal is sql.ErrNoRows — best-effort, keeping the
// not-found error path.
func (r dbRunnerLogResolver) latestRemovalCaptureID(runnerName string) (string, error) {
	if r.dataDir == "" {
		return "", sql.ErrNoRows
	}
	lines, err := readLastLines(removalsLogPath(r.dataDir), removalScanCap, removalLineMaxCap)
	if err != nil {
		return "", sql.ErrNoRows
	}
	for i := len(lines) - 1; i >= 0; i-- {
		var rec removalRecord
		if jsonErr := json.Unmarshal(lines[i], &rec); jsonErr != nil {
			continue
		}
		if rec.RunnerName != runnerName || !rec.Capture.OK || rec.Capture.File == "" {
			continue
		}
		id, idErr := captureIDFromPath(rec.Capture.File)
		if idErr != nil {
			continue
		}
		return id, nil
	}
	return "", sql.ErrNoRows
}
