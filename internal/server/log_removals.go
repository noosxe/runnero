package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"connectrpc.com/connect"
	supervisorv1 "github.com/noosxe/runnero/internal/pb/supervisor/v1"
)

// This file surfaces the structured removal decision records
// (<DATA_DIR>/logs/removals.jsonl, docs/28 §5.4) over RPC — the read half
// of docs/29 §5.1 implemented by ListRemovalRecords.

const (
	// removalPageDefault is the page size when page_size is unset.
	removalPageDefault = 50
	// removalPageMax is the hard server-side cap for page_size.
	removalPageMax = 200
	// removalScanCap bounds how many trailing lines one request may scan,
	// so a pathological removals.jsonl cannot turn one call into
	// unbounded IO (docs/29 §5.1).
	removalScanCap = 10_000
	// removalLineMaxCap bounds a single removals.jsonl record read.
	removalLineMaxCap = 1024 * 1024
)

// removalsLogPath returns <dataDir>/logs/removals.jsonl.
func removalsLogPath(dataDir string) string {
	return filepath.Join(dataDir, "logs", "removals.jsonl")
}

// removalCapture mirrors orchestrator.RemovalCapture's on-disk JSON schema.
type removalCapture struct {
	OK      bool   `json:"ok"`
	Bytes   int64  `json:"bytes,omitempty"`
	File    string `json:"file,omitempty"`
	Error   string `json:"error,omitempty"`
	Skipped string `json:"skipped,omitempty"`
}

// removalRecord mirrors orchestrator.RemovalRecord's on-disk JSON schema
// (docs/28 §5.4). It is duplicated here because internal/server cannot
// import internal/orchestrator (import cycle); the round-trip test in
// log_removals_test.go keeps the mirror in lockstep with the writer.
type removalRecord struct {
	TS           time.Time      `json:"ts"`
	BootID       string         `json:"boot_id,omitempty"`
	RunnerID     string         `json:"runner_id"`
	RunnerName   string         `json:"runner_name,omitempty"`
	PoolID       int64          `json:"pool_id,omitempty"`
	PoolName     string         `json:"pool_name,omitempty"`
	Container    string         `json:"container"`
	Reason       string         `json:"reason"`
	ProviderBusy bool           `json:"provider_busy,omitempty"`
	DeregError   string         `json:"dereg_error,omitempty"`
	ExitCode     *int           `json:"exit_code,omitempty"`
	Capture      removalCapture `json:"capture"`
}

// removalRecordSummary projects an on-disk removalRecord onto the wire
// summary. exit_code uses -1 as the unset sentinel (proto int32).
func removalRecordSummary(rec *removalRecord) *supervisorv1.RemovalRecordSummary {
	exitCode := int32(-1)
	if rec.ExitCode != nil {
		exitCode = int32(*rec.ExitCode)
	}
	return &supervisorv1.RemovalRecordSummary{
		Ts:           rec.TS.UTC().Format(time.RFC3339Nano),
		BootId:       rec.BootID,
		RunnerId:     rec.RunnerID,
		RunnerName:   rec.RunnerName,
		PoolId:       rec.PoolID,
		PoolName:     rec.PoolName,
		Reason:       rec.Reason,
		ProviderBusy: rec.ProviderBusy,
		DeregError:   rec.DeregError,
		ExitCode:     exitCode,
		CaptureOk:    rec.Capture.OK,
		CaptureBytes: rec.Capture.Bytes,
	}
}

// ListRemovalRecords reverse-scans removals.jsonl (newest record first)
// with optional filters and cursor pagination. The scan is capped at the
// newest removalScanCap lines per request; the cursor is the RFC3339Nano
// ts of the previous page's last record, and only strictly older records
// are returned on the next page (records sharing a ts fall to the earlier
// page — practically unreachable: the writer timestamps each record at
// write time with nanosecond precision).
func (s *LogService) ListRemovalRecords(ctx context.Context, req *connect.Request[supervisorv1.ListRemovalRecordsRequest]) (*connect.Response[supervisorv1.ListRemovalRecordsResponse], error) {
	// Validate time filters up front so malformed input is a client error,
	// not a silently empty page.
	var since, until, cursor time.Time
	var err error
	if req.Msg.Since != "" {
		if since, err = time.Parse(time.RFC3339, req.Msg.Since); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("since: %w", err))
		}
	}
	if req.Msg.Until != "" {
		if until, err = time.Parse(time.RFC3339, req.Msg.Until); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("until: %w", err))
		}
	}
	if req.Msg.Cursor != "" {
		if cursor, err = time.Parse(time.RFC3339Nano, req.Msg.Cursor); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("cursor: %w", err))
		}
	}
	if req.Msg.PageSize < 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("page_size must not be negative"))
	}
	pageSize := int(req.Msg.PageSize)
	if pageSize == 0 {
		pageSize = removalPageDefault
	}
	if pageSize > removalPageMax {
		pageSize = removalPageMax
	}

	lines, err := readLastLines(removalsLogPath(s.dataDir), removalScanCap, removalLineMaxCap)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return connect.NewResponse(&supervisorv1.ListRemovalRecordsResponse{}), nil
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("reading removal log: %w", err))
	}

	records := make([]*supervisorv1.RemovalRecordSummary, 0, pageSize)
	for i := len(lines) - 1; i >= 0 && len(records) < pageSize; i-- {
		var rec removalRecord
		if err := json.Unmarshal(lines[i], &rec); err != nil {
			continue // malformed line: skip, mirroring the sweeper's tolerance
		}
		// Cursor: only records strictly older than the previous page's
		// last ts.
		if !cursor.IsZero() && !rec.TS.Before(cursor) {
			continue
		}
		if req.Msg.PoolId != 0 && rec.PoolID != req.Msg.PoolId {
			continue
		}
		if req.Msg.RunnerId != "" && rec.RunnerID != req.Msg.RunnerId {
			continue
		}
		if req.Msg.Reason != "" && rec.Reason != req.Msg.Reason {
			continue
		}
		if !since.IsZero() && rec.TS.Before(since) {
			continue
		}
		if !until.IsZero() && rec.TS.After(until) {
			continue
		}
		records = append(records, removalRecordSummary(&rec))
	}

	// Classic keyset convention: a full page implies there may be more;
	// a short page ends the iteration. (A full page at the very oldest
	// end of the scan cap yields one empty follow-up page.)
	var nextCursor string
	if len(records) == pageSize {
		nextCursor = records[len(records)-1].Ts
	}

	return connect.NewResponse(&supervisorv1.ListRemovalRecordsResponse{
		Records:    records,
		NextCursor: nextCursor,
	}), nil
}

// readLastLines returns the last maxLines non-empty raw lines of a text file,
// oldest first. Memory is bounded by maxLines × maxLineBytes.
func readLastLines(path string, maxLines, maxLineBytes int) ([][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)

	ring := make([][]byte, 0, min(maxLines, 64))
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		ring = append(ring, append([]byte(nil), line...))
		if len(ring) > maxLines {
			ring = ring[1:]
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return ring, nil
}
