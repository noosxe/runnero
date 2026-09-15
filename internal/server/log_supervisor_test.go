package server_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	supervisorv1 "github.com/noosxe/runnero/internal/pb/supervisor/v1"
	"github.com/noosxe/runnero/internal/pb/supervisor/v1/supervisorv1connect"
)

// writeBootFile creates a boot log file in <dataDir>/logs/supervisor. The
// header line mirrors what logging.BootFileSink writes; extra lines are
// appended verbatim.
func writeBootFile(t *testing.T, dataDir, name string, header string, lines ...string) {
	t.Helper()
	dir := filepath.Join(dataDir, "logs", "supervisor")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir supervisor logs: %v", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("create boot file %s: %v", name, err)
	}
	defer func() { _ = f.Close() }()
	if header != "" {
		if _, err := fmt.Fprintln(f, header); err != nil {
			t.Fatalf("write header: %v", err)
		}
	}
	for _, line := range lines {
		if _, err := fmt.Fprintln(f, line); err != nil {
			t.Fatalf("write line: %v", err)
		}
	}
}

func bootHeaderLine(bootID, startedAt string) string {
	return fmt.Sprintf(`{"log":"runnero-supervisor","boot_id":%q,"started_at":%q,"version":"dev"}`, bootID, startedAt)
}

// firstReceiveError drives a server-streaming RPC to its first server
// response (connect streams lazily) and returns the stream error, if any.
func firstReceiveError(ctx context.Context, client supervisorv1connect.LogServiceClient, req *connect.Request[supervisorv1.StreamSupervisorLogRequest]) error {
	stream, err := client.StreamSupervisorLog(ctx, req)
	if err != nil {
		return err
	}
	for stream.Receive() {
	}
	return stream.Err()
}
func slogLine(level, msg string) string {
	return fmt.Sprintf(`{"time":"2026-09-15T10:00:00.123456789Z","level":%q,"msg":%q,"pool_id":7}`, level, msg)
}

// appendBootLine appends one line to an existing boot file (simulating a
// live write without truncating it).
func appendBootLine(t *testing.T, dataDir, name, line string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dataDir, "logs", "supervisor", name), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("append to boot file %s: %v", name, err)
	}
	defer func() { _ = f.Close() }()
	if _, err := fmt.Fprintln(f, line); err != nil {
		t.Fatalf("append line: %v", err)
	}
}

// compressGzipLines gzips JSONL lines into a capture payload.
func compressGzipLines(t *testing.T, lines []string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	for _, line := range lines {
		if _, err := fmt.Fprintln(w, line); err != nil {
			t.Fatalf("gzip write: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func TestListSupervisorLogs(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()

	prev := time.Unix(1737000000, 0).UTC()
	curr := time.Unix(1737003600, 0).UTC()

	// Previous boot: base file + one rotation. Current boot: base only.
	writeBootFile(t, dataDir, "boot-1737000000-9f2c11ab.ndjson", bootHeaderLine("9f2c11ab-full", prev.Format(time.RFC3339Nano)), slogLine("INFO", "older boot line"))
	writeBootFile(t, dataDir, "boot-1737000000-9f2c11ab-0001.ndjson", "", slogLine("WARN", "rotated line"))
	writeBootFile(t, dataDir, "boot-1737003600-abcdef01.ndjson", bootHeaderLine("abcdef01-full", curr.Format(time.RFC3339Nano)), slogLine("INFO", "current line"))

	env := newLogTestEnv(t, dataDir, "boot-1737003600-abcdef01.ndjson")
	req := connect.NewRequest(&supervisorv1.ListSupervisorLogsRequest{})
	req.Header().Set("Cookie", "session_token="+env.Token)

	res, err := env.Client.ListSupervisorLogs(ctx, req)
	if err != nil {
		t.Fatalf("ListSupervisorLogs failed: %v", err)
	}

	boots := res.Msg.Boots
	if len(boots) != 3 {
		t.Fatalf("expected 3 boot files, got %d: %+v", len(boots), boots)
	}

	// Newest boot first; within the old boot, newest rotation first.
	wantOrder := []string{
		"boot-1737003600-abcdef01.ndjson",
		"boot-1737000000-9f2c11ab-0001.ndjson",
		"boot-1737000000-9f2c11ab.ndjson",
	}
	for i, name := range wantOrder {
		if boots[i].File != name {
			t.Fatalf("order mismatch at %d: got %s, want %s", i, boots[i].File, name)
		}
	}

	current := boots[0]
	if !current.IsCurrent {
		t.Errorf("current boot not flagged: %+v", current)
	}
	if current.BootId != "abcdef01-full" {
		t.Errorf("boot id should come from the header, got %q", current.BootId)
	}
	if current.RotationSeq != 0 || current.SizeBytes <= 0 {
		t.Errorf("rotation seq/size mismatch: %+v", current)
	}
	if _, err := time.Parse(time.RFC3339, current.StartedAt); err != nil {
		t.Errorf("started_at not RFC3339: %q", current.StartedAt)
	}

	rotated := boots[1]
	if rotated.IsCurrent {
		t.Errorf("rotated file flagged current: %+v", rotated)
	}
	if rotated.RotationSeq != 1 {
		t.Errorf("rotation seq: got %d, want 1", rotated.RotationSeq)
	}
	// Rotation files inherit the boot's header metadata via the base file.
	if rotated.BootId != "9f2c11ab-full" {
		t.Errorf("rotated file should carry the base file's boot id, got %q", rotated.BootId)
	}

	// Empty / missing directory → empty listing, no error.
	emptyEnv := newLogTestEnv(t, t.TempDir(), "")
	emptyReq := connect.NewRequest(&supervisorv1.ListSupervisorLogsRequest{})
	emptyReq.Header().Set("Cookie", "session_token="+emptyEnv.Token)
	emptyRes, err := emptyEnv.Client.ListSupervisorLogs(ctx, emptyReq)
	if err != nil {
		t.Fatalf("missing dir should not error: %v", err)
	}
	if len(emptyRes.Msg.Boots) != 0 {
		t.Errorf("expected empty listing, got %d", len(emptyRes.Msg.Boots))
	}
}

func TestListSupervisorLogs_FilenameFallbackWithoutHeader(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()

	// A boot file whose first line is not a valid header (e.g. legacy or
	// corrupted) falls back to filename-derived metadata.
	writeBootFile(t, dataDir, "boot-1737000000-9f2c11ab.ndjson", "not json at all", slogLine("INFO", "line"))

	env := newLogTestEnv(t, dataDir, "boot-1737000000-9f2c11ab.ndjson")
	req := connect.NewRequest(&supervisorv1.ListSupervisorLogsRequest{})
	req.Header().Set("Cookie", "session_token="+env.Token)

	res, err := env.Client.ListSupervisorLogs(ctx, req)
	if err != nil {
		t.Fatalf("ListSupervisorLogs failed: %v", err)
	}
	if len(res.Msg.Boots) != 1 {
		t.Fatalf("expected 1 boot file, got %d", len(res.Msg.Boots))
	}
	b := res.Msg.Boots[0]
	if b.BootId != "9f2c11ab" {
		t.Errorf("fallback boot id: got %q, want 9f2c11ab", b.BootId)
	}
	wantStarted := time.Unix(1737000000, 0).UTC().Format(time.RFC3339)
	if b.StartedAt != wantStarted {
		t.Errorf("fallback started_at: got %q, want %q", b.StartedAt, wantStarted)
	}
}

func TestStreamSupervisorLog_TailReplay(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()

	writeBootFile(t, dataDir, "boot-1737000000-9f2c11ab.ndjson",
		bootHeaderLine("9f2c11ab", "2026-09-15T10:00:00Z"),
		slogLine("INFO", "line-1"),
		slogLine("WARN", "line-2"),
		"{broken json line", // malformed: passes through verbatim
		slogLine("ERROR", "line-3"),
	)

	env := newLogTestEnv(t, dataDir, "boot-1737000000-9f2c11ab.ndjson")
	req := connect.NewRequest(&supervisorv1.StreamSupervisorLogRequest{
		File:      "boot-1737000000-9f2c11ab.ndjson",
		TailLines: 3,
	})
	req.Header().Set("Cookie", "session_token="+env.Token)

	stream, err := env.Client.StreamSupervisorLog(ctx, req)
	if err != nil {
		t.Fatalf("StreamSupervisorLog failed: %v", err)
	}
	defer func() { _ = stream.Close() }()

	var chunks []*supervisorv1.LogChunk
	for stream.Receive() {
		chunks = append(chunks, stream.Msg())
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("stream error: %v", err)
	}

	if len(chunks) != 3 {
		t.Fatalf("expected 3 replayed chunks, got %d: %+v", len(chunks), chunks)
	}
	if chunks[0].Stream != "WARN" || chunks[0].Content != "line-2 pool_id=7" {
		t.Errorf("chunk 0 mismatch: %+v", chunks[0])
	}
	// Malformed line passes through verbatim, stream is stdout, ts empty.
	if chunks[1].Content != "{broken json line" {
		t.Errorf("malformed line should pass through, got %q", chunks[1].Content)
	}
	if chunks[2].Stream != "ERROR" || chunks[2].Content != "line-3 pool_id=7" {
		t.Errorf("chunk 2 mismatch: %+v", chunks[2])
	}
	if chunks[0].Timestamp == "" {
		t.Errorf("slog records carry their time: %+v", chunks[0])
	}
}

func TestStreamSupervisorLog_FollowAndValidation(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()

	current := "boot-1737003600-abcdef01.ndjson"
	writeBootFile(t, dataDir, current,
		bootHeaderLine("abcdef01", "2026-09-15T11:00:00Z"),
		slogLine("INFO", "first"),
	)

	env := newLogTestEnv(t, dataDir, current)

	// Follow on a previous boot is rejected. Server-streaming RPCs start
	// lazily, so the handler error surfaces on the first Receive.
	writeBootFile(t, dataDir, "boot-1737000000-9f2c11ab.ndjson", bootHeaderLine("9f2c11ab", "2026-09-15T10:00:00Z"))
	oldReq := connect.NewRequest(&supervisorv1.StreamSupervisorLogRequest{
		File:   "boot-1737000000-9f2c11ab.ndjson",
		Follow: true,
	})
	oldReq.Header().Set("Cookie", "session_token="+env.Token)
	if err := firstReceiveError(ctx, env.Client, oldReq); err == nil || !strings.Contains(err.Error(), "failed_precondition") {
		t.Fatalf("follow on previous boot should be failed_precondition, got %v", err)
	}

	// Invalid file names are client errors.
	for _, bad := range []string{"", "../escape.ndjson", "/abs/path.ndjson", "boot-../../x.ndjson"} {
		badReq := connect.NewRequest(&supervisorv1.StreamSupervisorLogRequest{File: bad})
		badReq.Header().Set("Cookie", "session_token="+env.Token)
		if err := firstReceiveError(ctx, env.Client, badReq); err == nil || !strings.Contains(err.Error(), "invalid_argument") {
			t.Errorf("file %q should be invalid_argument, got %v", bad, err)
		}
	}

	// Unknown but well-formed file name is NotFound.
	missingReq := connect.NewRequest(&supervisorv1.StreamSupervisorLogRequest{File: "boot-1111111111-cafebabe.ndjson"})
	missingReq.Header().Set("Cookie", "session_token="+env.Token)
	if err := firstReceiveError(ctx, env.Client, missingReq); err == nil || !strings.Contains(err.Error(), "not_found") {
		t.Fatalf("unknown file should be not_found, got %v", err)
	}

	// Follow on the current boot: replay first, then appended records.
	followCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	followReq := connect.NewRequest(&supervisorv1.StreamSupervisorLogRequest{
		File:      current,
		Follow:    true,
		TailLines: 10,
	})
	followReq.Header().Set("Cookie", "session_token="+env.Token)

	stream, err := env.Client.StreamSupervisorLog(followCtx, followReq)
	if err != nil {
		t.Fatalf("follow stream failed: %v", err)
	}
	defer func() { _ = stream.Close() }()

	// Replay arrives immediately: the header record renders first, then
	// the boot's own log lines.
	if !stream.Receive() {
		t.Fatalf("expected replayed record, got stream error: %v", stream.Err())
	}
	if stream.Msg().Content != "boot_id=abcdef01 log=runnero-supervisor version=dev" {
		t.Fatalf("header replay mismatch: %+v", stream.Msg())
	}
	if !stream.Receive() {
		t.Fatalf("expected second replayed record, got stream error: %v", stream.Err())
	}
	if stream.Msg().Content != "first pool_id=7" {
		t.Fatalf("replayed record mismatch: %+v", stream.Msg())
	}

	// Append a record; the follow loop must deliver it.
	time.Sleep(50 * time.Millisecond)
	appendBootLine(t, dataDir, current, slogLine("INFO", "appended"))

	received := make(chan *supervisorv1.LogChunk, 1)
	go func() {
		if stream.Receive() {
			received <- stream.Msg()
		}
	}()
	select {
	case chunk := <-received:
		if chunk.Content != "appended pool_id=7" {
			t.Fatalf("appended record mismatch: %+v", chunk)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("follow did not deliver the appended record in time")
	}

	// Clean cancel.
	cancel()
}

func TestGetRunnerLogs_InputValidation(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()

	env := newLogTestEnv(t, dataDir, "boot-99-abcdef01.ndjson")

	// Path traversal in runner_id is rejected outright (docs/29 §5.2).
	for _, bad := range []string{"", "../../etc/passwd", "/absolute", "runner/../logs"} {
		req := connect.NewRequest(&supervisorv1.GetRunnerLogsRequest{RunnerId: bad})
		req.Header().Set("Cookie", "session_token="+env.Token)
		if _, err := env.Client.GetRunnerLogs(ctx, req); err == nil || !strings.Contains(err.Error(), "invalid_argument") {
			t.Errorf("runner id %q should be invalid_argument, got %v", bad, err)
		}
	}

	// Negative tail_lines rejected.
	req := connect.NewRequest(&supervisorv1.GetRunnerLogsRequest{RunnerId: "runner-1", TailLines: -5})
	req.Header().Set("Cookie", "session_token="+env.Token)
	if _, err := env.Client.GetRunnerLogs(ctx, req); err == nil || !strings.Contains(err.Error(), "invalid_argument") {
		t.Errorf("negative tail_lines should be invalid_argument, got %v", err)
	}
}

func TestGetRunnerLogs_TailLines(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()

	logsDir := filepath.Join(dataDir, "logs")
	if err := os.MkdirAll(logsDir, 0o700); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}

	// Build a gzipped capture with 10 entries; request only the last 3.
	var lines []string
	for i := 0; i < 10; i++ {
		entry, _ := json.Marshal(map[string]any{
			"timestamp": fmt.Sprintf("2026-09-15T10:00:%02dZ", i),
			"stream":    "stdout",
			"content":   fmt.Sprintf("entry-%d", i),
		})
		lines = append(lines, string(entry))
	}
	capture := compressGzipLines(t, lines)
	if err := os.WriteFile(filepath.Join(logsDir, "runner-1.log.jsonl.gz"), capture, 0o600); err != nil {
		t.Fatalf("write capture: %v", err)
	}

	env := newLogTestEnv(t, dataDir, "boot-99-abcdef01.ndjson")
	req := connect.NewRequest(&supervisorv1.GetRunnerLogsRequest{RunnerId: "runner-1", TailLines: 3})
	req.Header().Set("Cookie", "session_token="+env.Token)

	res, err := env.Client.GetRunnerLogs(ctx, req)
	if err != nil {
		t.Fatalf("GetRunnerLogs failed: %v", err)
	}
	got := res.Msg.Lines
	if len(got) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(got))
	}
	for i, want := range []string{"entry-7", "entry-8", "entry-9"} {
		if got[i].Content != want {
			t.Errorf("line %d: got %q, want %q", i, got[i].Content, want)
		}
	}
}
