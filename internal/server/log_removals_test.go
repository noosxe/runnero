package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/noosxe/runnero/internal/orchestrator"
	supervisorv1 "github.com/noosxe/runnero/internal/pb/supervisor/v1"
	"github.com/noosxe/runnero/internal/pb/supervisor/v1/supervisorv1connect"
	"github.com/noosxe/runnero/internal/server"
)

// logTestEnv is an authenticated LogService test client bound to a temp
// data dir with a mock boot-log identity.
type logTestEnv struct {
	Client supervisorv1connect.LogServiceClient
	Token  string
}

// newLogTestEnv boots an authenticated server and returns a ready client.
// bootCurrent is the boot file the mock supervisor reports as current.
func newLogTestEnv(t *testing.T, dataDir, bootCurrent string) *logTestEnv {
	t.Helper()
	ctx := context.Background()
	database := setupTestDB(t)

	srv := server.New(server.Options{
		Port:    8080,
		AuthDB:  database,
		DataDir: dataDir,
		Session: testSessionConfig(),
		BootLog: &mockBootLogFile{current: bootCurrent},
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	authClient := supervisorv1connect.NewAuthServiceClient(ts.Client(), ts.URL)
	if _, err := authClient.SetupAdmin(ctx, connect.NewRequest(&supervisorv1.SetupAdminRequest{
		Username: "admin",
		Password: "password123456",
	})); err != nil {
		t.Fatalf("SetupAdmin failed: %v", err)
	}
	loginRes, err := authClient.Login(ctx, connect.NewRequest(&supervisorv1.LoginRequest{
		Username: "admin",
		Password: "password123456",
	}))
	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}
	cookie := loginRes.Header().Get("Set-Cookie")

	return &logTestEnv{
		Client: supervisorv1connect.NewLogServiceClient(ts.Client(), ts.URL),
		Token:  strings.Split(strings.Split(cookie, ";")[0], "=")[1],
	}
}

// mockBootLogFile fakes the supervisor's boot-file identity.
type mockBootLogFile struct{ current string }

func (m *mockBootLogFile) CurrentBootFile() string { return m.current }

// writeRemovalRecord appends one removals.jsonl record with the given
// attributes at ts.
func writeRemovalRecord(t *testing.T, dataDir string, ts time.Time, poolID int64, poolName, runnerID, reason string, providerBusy bool, exitCode *int) {
	t.Helper()
	path := filepath.Join(dataDir, "logs", "removals.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	line, err := json.Marshal(map[string]any{
		"ts":            ts.Format(time.RFC3339Nano),
		"boot_id":       "9f2c11ab",
		"runner_id":     runnerID,
		"runner_name":   runnerID,
		"pool_id":       poolID,
		"pool_name":     poolName,
		"container":     runnerID + "-ctr",
		"reason":        reason,
		"provider_busy": providerBusy,
		"exit_code":     exitCode,
		"capture":       map[string]any{"ok": true, "bytes": 1234},
	})
	if err != nil {
		t.Fatalf("marshal removal record: %v", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open removals.jsonl: %v", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := fmt.Fprintf(f, "%s\n", line); err != nil {
		t.Fatalf("append removal record: %v", err)
	}
}

func TestListRemovalRecords_ReverseChronologicalWithFields(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()

	base := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	exit := 137
	// Oldest → newest.
	writeRemovalRecord(t, dataDir, base, 1, "alpha", "runner-a", "reap", false, nil)
	writeRemovalRecord(t, dataDir, base.Add(time.Second), 2, "beta", "runner-b", "task-exit", true, &exit)

	env := newLogTestEnv(t, dataDir, "boot-99-abcdef01.ndjson")
	req := connect.NewRequest(&supervisorv1.ListRemovalRecordsRequest{})
	req.Header().Set("Cookie", "session_token="+env.Token)

	res, err := env.Client.ListRemovalRecords(ctx, req)
	if err != nil {
		t.Fatalf("ListRemovalRecords failed: %v", err)
	}

	recs := res.Msg.Records
	if len(recs) != 2 {
		t.Fatalf("expected 2 records, got %d", len(recs))
	}
	// Newest first.
	if recs[0].RunnerId != "runner-b" || recs[1].RunnerId != "runner-a" {
		t.Fatalf("order mismatch: %+v", recs)
	}
	b := recs[0]
	if b.Reason != "task-exit" || !b.ProviderBusy || b.ExitCode != 137 || !b.CaptureOk || b.CaptureBytes != 1234 {
		t.Errorf("runner-b record mismatch: %+v", b)
	}
	if b.PoolId != 2 || b.PoolName != "beta" || b.BootId != "9f2c11ab" {
		t.Errorf("runner-b metadata mismatch: %+v", b)
	}
	a := recs[1]
	if a.ExitCode != -1 || a.ProviderBusy {
		t.Errorf("runner-a defaults mismatch (want exit_code -1, not busy): %+v", a)
	}
	if res.Msg.NextCursor != "" {
		t.Errorf("short page must not carry a cursor, got %q", res.Msg.NextCursor)
	}
}

func TestListRemovalRecords_Filters(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()

	base := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	writeRemovalRecord(t, dataDir, base, 1, "alpha", "runner-a", "reap", false, nil)
	writeRemovalRecord(t, dataDir, base.Add(time.Second), 1, "alpha", "runner-b", "manual", false, nil)
	writeRemovalRecord(t, dataDir, base.Add(2*time.Second), 2, "beta", "runner-a", "reap", true, nil)
	writeRemovalRecord(t, dataDir, base.Add(3*time.Second), 3, "gamma", "runner-c", "reap", false, nil)

	env := newLogTestEnv(t, dataDir, "boot-99-abcdef01.ndjson")

	run := func(msg *supervisorv1.ListRemovalRecordsRequest) []string {
		t.Helper()
		req := connect.NewRequest(msg)
		req.Header().Set("Cookie", "session_token="+env.Token)
		res, err := env.Client.ListRemovalRecords(ctx, req)
		if err != nil {
			t.Fatalf("ListRemovalRecords(%+v) failed: %v", msg, err)
		}
		var ids []string
		for _, r := range res.Msg.Records {
			ids = append(ids, r.RunnerId+"/"+r.Reason)
		}
		return ids
	}

	if got := run(&supervisorv1.ListRemovalRecordsRequest{PoolId: 1}); len(got) != 2 {
		t.Errorf("pool filter: got %v", got)
	}
	if got := run(&supervisorv1.ListRemovalRecordsRequest{RunnerId: "runner-a"}); len(got) != 2 {
		t.Errorf("runner filter: got %v", got)
	}
	if got := run(&supervisorv1.ListRemovalRecordsRequest{Reason: "manual"}); len(got) != 1 || got[0] != "runner-b/manual" {
		t.Errorf("reason filter: got %v", got)
	}
	since := base.Add(1500 * time.Millisecond).Format(time.RFC3339)
	if got := run(&supervisorv1.ListRemovalRecordsRequest{Since: since}); len(got) != 3 {
		t.Errorf("since filter: got %v", got)
	}
	until := base.Add(2500 * time.Millisecond).Format(time.RFC3339)
	if got := run(&supervisorv1.ListRemovalRecordsRequest{Until: until}); len(got) != 3 {
		t.Errorf("until filter: got %v", got)
	}
	if got := run(&supervisorv1.ListRemovalRecordsRequest{PoolId: 1, Reason: "manual"}); len(got) != 1 || got[0] != "runner-b/manual" {
		t.Errorf("combined filters: got %v", got)
	}

	// Malformed time filters are client errors.
	badReq := connect.NewRequest(&supervisorv1.ListRemovalRecordsRequest{Since: "not-a-time"})
	badReq.Header().Set("Cookie", "session_token="+env.Token)
	if _, err := env.Client.ListRemovalRecords(ctx, badReq); err == nil || !strings.Contains(err.Error(), "invalid_argument") {
		t.Errorf("malformed since should be invalid_argument, got %v", err)
	}
}

func TestListRemovalRecords_CursorPagination(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()

	base := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		writeRemovalRecord(t, dataDir, base.Add(time.Duration(i)*time.Millisecond), 1, "alpha", fmt.Sprintf("runner-%d", i), "reap", false, nil)
	}

	env := newLogTestEnv(t, dataDir, "boot-99-abcdef01.ndjson")

	var collected []string
	cursor := ""
	pages := 0
	for {
		req := connect.NewRequest(&supervisorv1.ListRemovalRecordsRequest{PageSize: 2, Cursor: cursor})
		req.Header().Set("Cookie", "session_token="+env.Token)
		res, err := env.Client.ListRemovalRecords(ctx, req)
		if err != nil {
			t.Fatalf("page %d failed: %v", pages, err)
		}
		for _, r := range res.Msg.Records {
			collected = append(collected, r.RunnerId)
		}
		pages++
		if res.Msg.NextCursor == "" || pages > 10 {
			break
		}
		cursor = res.Msg.NextCursor
	}

	if pages != 3 {
		t.Errorf("expected 3 pages (2+2+1), got %d", pages)
	}
	// Newest → oldest: runner-4, 3, 2, 1, 0.
	want := []string{"runner-4", "runner-3", "runner-2", "runner-1", "runner-0"}
	if len(collected) != len(want) {
		t.Fatalf("collected %v, want %v", collected, want)
	}
	for i := range want {
		if collected[i] != want[i] {
			t.Fatalf("collected %v, want %v", collected, want)
		}
	}
}

func TestListRemovalRecords_MalformedLinesSkippedAndMissingFileEmpty(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()

	// Missing file entirely → empty response, no error.
	env := newLogTestEnv(t, dataDir, "boot-99-abcdef01.ndjson")
	req := connect.NewRequest(&supervisorv1.ListRemovalRecordsRequest{})
	req.Header().Set("Cookie", "session_token="+env.Token)
	res, err := env.Client.ListRemovalRecords(ctx, req)
	if err != nil {
		t.Fatalf("empty listing should not error: %v", err)
	}
	if len(res.Msg.Records) != 0 {
		t.Errorf("expected no records, got %d", len(res.Msg.Records))
	}

	// Malformed lines are skipped, valid ones still returned.
	path := filepath.Join(dataDir, "logs", "removals.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	ts := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	rec, _ := json.Marshal(map[string]any{"ts": ts.Format(time.RFC3339Nano), "runner_id": "runner-ok", "reason": "reap", "capture": map[string]any{}})
	content := "}{ broken json\n\n" + string(rec) + "\nnot json either\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write removals.jsonl: %v", err)
	}

	res, err = env.Client.ListRemovalRecords(ctx, req)
	if err != nil {
		t.Fatalf("malformed-tolerant read failed: %v", err)
	}
	if len(res.Msg.Records) != 1 || res.Msg.Records[0].RunnerId != "runner-ok" {
		t.Errorf("expected 1 valid record, got %+v", res.Msg.Records)
	}
}

// TestListRemovalRecords_WriterSchemaRoundTrip is the lockstep guard
// between the removal-log writer (orchestrator.RemovalRecord) and this
// read side: a fully populated record, marshaled exactly the way
// RemovalLogger writes it, must project onto the wire with every field
// intact.
func TestListRemovalRecords_WriterSchemaRoundTrip(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()

	exitCode := 137
	ts := time.Date(2026, 9, 15, 12, 34, 56, 789000000, time.UTC)
	rec := orchestrator.RemovalRecord{
		TS:           ts,
		BootID:       "9f2c11ab",
		RunnerID:     "runnero-pool1-abc123",
		RunnerName:   "runnero-pool1-abc123",
		PoolID:       7,
		PoolName:     "pool1",
		Container:    "runnero-pool1-abc123-ctr",
		Reason:       "task-exit",
		ProviderBusy: true,
		DeregError:   "rate limited",
		ExitCode:     &exitCode,
		Capture: orchestrator.RemovalCapture{
			OK:    true,
			Bytes: 4096,
			File:  "runnero-pool1-abc123.log.jsonl.gz",
		},
	}
	raw, err := json.Marshal(&rec)
	if err != nil {
		t.Fatalf("marshal RemovalRecord: %v", err)
	}

	path := filepath.Join(dataDir, "logs", "removals.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		t.Fatalf("write removals.jsonl: %v", err)
	}

	env := newLogTestEnv(t, dataDir, "boot-99-abcdef01.ndjson")
	req := connect.NewRequest(&supervisorv1.ListRemovalRecordsRequest{})
	req.Header().Set("Cookie", "session_token="+env.Token)
	res, err := env.Client.ListRemovalRecords(ctx, req)
	if err != nil {
		t.Fatalf("ListRemovalRecords failed: %v", err)
	}
	if len(res.Msg.Records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(res.Msg.Records))
	}
	got := res.Msg.Records[0]

	wantTs := ts.UTC().Format(time.RFC3339Nano)
	if got.Ts != wantTs {
		t.Errorf("ts: got %q, want %q", got.Ts, wantTs)
	}
	if got.BootId != "9f2c11ab" || got.RunnerId != "runnero-pool1-abc123" ||
		got.RunnerName != "runnero-pool1-abc123" || got.PoolId != 7 ||
		got.PoolName != "pool1" || got.Reason != "task-exit" ||
		!got.ProviderBusy || got.DeregError != "rate limited" ||
		got.ExitCode != 137 || !got.CaptureOk || got.CaptureBytes != 4096 {
		t.Errorf("wire projection drifted from writer schema: %+v", got)
	}
}
