package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	supervisorv1 "github.com/noosxe/runnero/internal/pb/supervisor/v1"
)

// This file implements the read side of the persisted supervisor boot logs
// (docs/29, RUN-218): boot files under
// <DATA_DIR>/logs/supervisor/boot-*.ndjson, surfaced by ListSupervisorLogs
// and StreamSupervisorLog. Removal records live in log_removals.go, the
// runner capture reads in log.go.

const (
	// bootFileReplayDefault is the tail replay size when tail_lines is unset.
	bootFileReplayDefault = 500
	// bootFileReplayMax is the hard server-side cap for tail_lines.
	bootFileReplayMax = 5000
	// bootFollowPollInterval is how often a followed boot file is polled
	// for appended records.
	bootFollowPollInterval = 300 * time.Millisecond
	// bootHeaderReadCap bounds the self-orientation header line read.
	bootHeaderReadCap = 8192
)

// logResourceNameRE bounds every client-supplied name used in a filesystem
// path: printable filesystem-safe characters only, no separators, no leading
// dot. This closes the previously unvalidated GetRunnerLogs path
// (docs/29 §5.2): a runner id containing "../" used to escape the logs
// directory before reaching filepath.Join.
var logResourceNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,128}$`)

// safeLogResourceName validates and normalizes a client-supplied file or
// runner name before it is used in a path.
func safeLogResourceName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if !logResourceNameRE.MatchString(trimmed) {
		return "", fmt.Errorf("invalid resource name %q", trimmed)
	}
	return filepath.Base(trimmed), nil
}

// BootLogFile identifies the boot file this supervisor process is currently
// appending to. *logging.BootFileSink satisfies it; kept as an interface to
// mirror the LogStreamer seam so handlers stay mock-based in tests
// (docs/29 §5.2).
type BootLogFile interface {
	// CurrentBootFile returns the base name of the boot log file currently
	// being appended to (e.g. "boot-1737000000-9f2c11ab-0002.ndjson"),
	// or "" if unknown.
	CurrentBootFile() string
}

// bootFileNameRE parses boot file base names written by logging.BootFileSink:
// "boot-<unix>-<bootid8>.ndjson" with optional "-NNNN" rotation suffix.
// bootid is non-greedy so a trailing -NNNN is preferred as the rotation
// counter; genuine boot ids ending in -NNNN are disambiguated against the
// directory listing in classifyBootFiles.
var bootFileNameRE = regexp.MustCompile(`^boot-(\d{10,})-(.+?)(?:-(\d{4}))?\.ndjson$`)

// bootFileRef is one classified boot file on disk.
type bootFileRef struct {
	name string
	unix int64
	id   string
	seq  int
}

// formatRotationSeq renders a rotation counter the way
// logging.BootFileSink.rotationSuffix writes it (zero-padded, 4 digits).
func formatRotationSeq(seq int) string {
	return fmt.Sprintf("%04d", seq)
}

// parseBootFileName extracts (unix ts, boot id, rotation seq) from a boot
// file base name.
func parseBootFileName(name string) (int64, string, int, bool) {
	m := bootFileNameRE.FindStringSubmatch(name)
	if m == nil {
		return 0, "", 0, false
	}
	unix, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, "", 0, false
	}
	seq := 0
	if m[3] != "" {
		parsed, err := strconv.Atoi(m[3])
		if err != nil {
			return 0, "", 0, false
		}
		seq = parsed
	}
	return unix, m[2], seq, true
}

// classifyBootFiles parses raw directory entry names into boot file refs,
// resolving the "-NNNN" ambiguity: a trailing digit group is a rotation
// counter only when a base file with the shortened boot id also exists;
// otherwise the digits belong to the boot id itself and the file is a base
// file (seq 0).
func classifyBootFiles(names []string) []bootFileRef {
	nameSet := make(map[string]bool, len(names))
	for _, n := range names {
		nameSet[n] = true
	}

	refs := make([]bootFileRef, 0, len(names))
	for _, name := range names {
		unix, id, seq, ok := parseBootFileName(name)
		if !ok {
			continue
		}
		if seq > 0 {
			baseName := fmt.Sprintf("boot-%d-%s.ndjson", unix, id)
			if !nameSet[baseName] {
				// No base file for the shortened id: the digit group is
				// part of the boot id, not a rotation counter.
				id = id + "-" + formatRotationSeq(seq)
				seq = 0
			}
		}
		refs = append(refs, bootFileRef{name: name, unix: unix, id: id, seq: seq})
	}
	return refs
}

// supervisorLogsDir returns <dataDir>/logs/supervisor.
func supervisorLogsDir(dataDir string) string {
	return filepath.Join(dataDir, "logs", "supervisor")
}

// resolveBootFile validates that a client-supplied boot file name exists in
// dir, applying the same rotation ambiguity rule as classifyBootFiles: the
// name is accepted only when its on-disk interpretation is consistent
// (base file present, or rotation with its base file present, or base file
// whose boot id itself ends in the digit group).
func resolveBootFile(dir, name string) error {
	unix, id, seq, ok := parseBootFileName(name)
	if !ok {
		return fmt.Errorf("not a boot log file name")
	}
	if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
		return fmt.Errorf("boot file %q not found", name)
	}
	if seq == 0 {
		return nil
	}
	// Rotation interpretation: consistent only with a base file present.
	if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("boot-%d-%s.ndjson", unix, id))); err == nil {
		return nil
	}
	// Otherwise the digit group must belong to the boot id: the name is
	// then a base file, already verified to exist above.
	return nil
}

// readBootHeader reads the self-orientation header line (the first record
// of a boot's base file, written by OpenBootFileSink) and returns its
// boot_id and started_at. Tolerant: any failure returns false and the
// caller falls back to filename-derived values.
func readBootHeader(path string) (bootID, startedAt string, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", false
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, bootHeaderReadCap), bootHeaderReadCap)
	if !scanner.Scan() {
		return "", "", false
	}
	var header struct {
		BootID    string `json:"boot_id"`
		StartedAt string `json:"started_at"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &header); err != nil {
		return "", "", false
	}
	return header.BootID, header.StartedAt, header.BootID != "" || header.StartedAt != ""
}

// currentBootFile answers which boot file belongs to the running process.
// With a wired BootLogFile the answer is authoritative; without one (tests,
// persistence disabled) the newest boot file on disk is the best-effort
// approximation.
func (s *LogService) currentBootFile(refs []bootFileRef) string {
	if s.bootLog != nil {
		if name := s.bootLog.CurrentBootFile(); name != "" {
			return name
		}
	}
	var best string
	var bestUnix int64
	bestSeq := -1
	for _, ref := range refs {
		if best == "" || ref.unix > bestUnix || (ref.unix == bestUnix && ref.seq > bestSeq) {
			best, bestUnix, bestSeq = ref.name, ref.unix, ref.seq
		}
	}
	return best
}

// ListSupervisorLogs enumerates the retained supervisor boot files with
// orientation metadata (docs/29 §5.1). Stat-only plus a one-line header
// read per boot; a missing directory yields an empty listing, not an error.
func (s *LogService) ListSupervisorLogs(ctx context.Context, req *connect.Request[supervisorv1.ListSupervisorLogsRequest]) (*connect.Response[supervisorv1.ListSupervisorLogsResponse], error) {
	dir := supervisorLogsDir(s.dataDir)
	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return connect.NewResponse(&supervisorv1.ListSupervisorLogsResponse{}), nil
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("listing boot log directory: %w", err))
	}

	var names []string
	for _, e := range dirEntries {
		if e.IsDir() {
			continue
		}
		if _, _, _, ok := parseBootFileName(e.Name()); ok {
			names = append(names, e.Name())
		}
	}
	refs := classifyBootFiles(names)
	current := s.currentBootFile(refs)

	boots := make([]*supervisorv1.SupervisorBootLog, 0, len(refs))
	for _, ref := range refs {
		info, err := os.Stat(filepath.Join(dir, ref.name))
		if err != nil {
			continue // raced with the retention sweeper; skip
		}

		// Boot id/started_at come from the self-orientation header of the
		// boot's base file when readable; filename-derived values are the
		// fallback (docs/29 §5.1).
		bootID := ref.id
		startedAt := time.Unix(ref.unix, 0).UTC().Format(time.RFC3339)
		if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("boot-%d-%s.ndjson", ref.unix, ref.id))); err == nil {
			if hID, hStart, ok := readBootHeader(filepath.Join(dir, fmt.Sprintf("boot-%d-%s.ndjson", ref.unix, ref.id))); ok {
				if hID != "" {
					bootID = hID
				}
				if hStart != "" {
					startedAt = hStart
				}
			}
		}

		boots = append(boots, &supervisorv1.SupervisorBootLog{
			File:        ref.name,
			BootId:      bootID,
			StartedAt:   startedAt,
			SizeBytes:   info.Size(),
			RotationSeq: int32(ref.seq),
			IsCurrent:   ref.name == current,
		})
	}

	// Newest boot first; within a boot, newest rotation first.
	sort.Slice(boots, func(i, j int) bool {
		ui, _, si, _ := parseBootFileName(boots[i].File)
		uj, _, sj, _ := parseBootFileName(boots[j].File)
		if ui != uj {
			return ui > uj
		}
		if si != sj {
			return si > sj
		}
		return boots[i].File > boots[j].File
	})

	return connect.NewResponse(&supervisorv1.ListSupervisorLogsResponse{Boots: boots}), nil
}

// StreamSupervisorLog server-streams one boot file as LogChunks: the last
// tail_lines records are replayed, then (only for the current boot, and
// only when follow is set) appended records are followed live.
// Previous boot files are immutable; following them is rejected.
func (s *LogService) StreamSupervisorLog(ctx context.Context, req *connect.Request[supervisorv1.StreamSupervisorLogRequest], stream *connect.ServerStream[supervisorv1.LogChunk]) error {
	name, err := safeLogResourceName(req.Msg.File)
	if err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	if req.Msg.TailLines < 0 {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("tail_lines must not be negative"))
	}
	tail := req.Msg.TailLines
	if tail == 0 {
		tail = bootFileReplayDefault
	}
	if tail > bootFileReplayMax {
		tail = bootFileReplayMax
	}

	dir := supervisorLogsDir(s.dataDir)
	if err := resolveBootFile(dir, name); err != nil {
		return connect.NewError(connect.CodeNotFound, err)
	}
	path := filepath.Join(dir, name)

	// Following requires an authoritative current-boot answer: without a
	// wired BootLogFile the guess could silently never follow, so refuse
	// (fail closed).
	if req.Msg.Follow && (s.bootLog == nil || s.bootLog.CurrentBootFile() != name) {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("cannot follow a previous boot; only the current boot file can be followed"))
	}

	// Tail replay: keep the last `tail` records before streaming.
	chunks, offset, err := readBootTail(path, int(tail))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("reading boot log: %w", err))
	}
	for _, chunk := range chunks {
		if err := stream.Send(chunk); err != nil {
			return err
		}
	}

	if !req.Msg.Follow {
		return nil
	}
	return followBootFile(ctx, path, offset, stream)
}

// bootTailLine is one non-empty ndjson record with its exact byte offset.
type bootTailLine struct {
	data   []byte
	offset int64
}

// readBootTail returns the last n parsed records of an ndjson boot file and
// the byte offset where the file ends — the resume point for live
// following, so appended records are delivered exactly once after the
// replayed tail.
func readBootTail(path string, n int) ([]*supervisorv1.LogChunk, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = f.Close() }()

	reader := bufio.NewReaderSize(f, 64*1024)
	tail := make([]bootTailLine, 0, min(n, 64))
	lineStart := int64(0)
	for {
		line, readErr := reader.ReadBytes('\n')
		trimmed := bytes.TrimRight(line, "\r\n")
		if len(bytes.TrimSpace(trimmed)) > 0 {
			tail = append(tail, bootTailLine{data: append([]byte(nil), trimmed...), offset: lineStart})
			if len(tail) > n {
				tail = tail[1:]
			}
		}
		lineStart += int64(len(line))
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return nil, 0, readErr
		}
	}

	chunks := make([]*supervisorv1.LogChunk, 0, len(tail))
	for _, l := range tail {
		chunks = append(chunks, parseBootLogLine(l.data))
	}
	// lineStart has consumed every byte including terminators: it is the
	// current EOF, i.e. where followBootFile resumes.
	return chunks, lineStart, nil
}

// parseBootLogLine converts one ndjson boot record (slog JSON: time, level,
// msg, attrs; or the boot header line) into a LogChunk: the level rides in
// stream, the message and remaining attributes are rendered as
// "msg key=value ..." text (docs/29 §5.1). A malformed record passes
// through verbatim so corruption stays visible in the viewer.
func parseBootLogLine(raw []byte) *supervisorv1.LogChunk {
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(raw), &record); err != nil {
		return &supervisorv1.LogChunk{Stream: "stdout", Content: string(bytes.TrimSpace(raw))}
	}

	str := func(v any) string {
		s, _ := v.(string)
		return s
	}

	ts := str(record["time"])
	if ts == "" {
		ts = str(record["started_at"])
	}
	level := strings.ToUpper(str(record["level"]))
	if level == "" {
		level = "stdout"
	}

	var parts []string
	if msg := str(record["msg"]); msg != "" {
		parts = append(parts, msg)
	}
	keys := make([]string, 0, len(record))
	for k := range record {
		switch k {
		case "time", "level", "msg", "started_at":
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, record[k]))
	}

	return &supervisorv1.LogChunk{
		Timestamp: ts,
		Stream:    level,
		Content:   strings.Join(parts, " "),
	}
}

// followBootFile polls a (current) boot file for appended records and
// streams them until the client cancels, the file stops growing because the
// boot rotated past it, or the file disappears (retention sweep / rename).
func followBootFile(ctx context.Context, path string, offset int64, stream *connect.ServerStream[supervisorv1.LogChunk]) error {
	ticker := time.NewTicker(bootFollowPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}

		info, err := os.Stat(path)
		if err != nil {
			return nil // rotated (renamed) or swept: the followed file is done
		}
		size := info.Size()
		if size <= offset {
			continue
		}

		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			_ = f.Close()
			return nil
		}
		data, err := io.ReadAll(io.LimitReader(f, size-offset))
		_ = f.Close()
		if err != nil {
			return nil
		}
		offset += int64(len(data))

		for _, line := range bytes.Split(data, []byte("\n")) {
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			if err := stream.Send(parseBootLogLine(line)); err != nil {
				return err
			}
		}
	}
}
