package server_test

import (
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/noosxe/runnero/internal/db"
	supervisorv1 "github.com/noosxe/runnero/internal/pb/supervisor/v1"
	supervisorv1connect "github.com/noosxe/runnero/internal/pb/supervisor/v1/supervisorv1connect"
	"github.com/noosxe/runnero/internal/server"
)

var seedJobRetentionPathSeq atomic.Int64

// seedJobRetentionPath inserts a closed job_history row for runnerName with
// the given retention path and completion time (RUN-252 fixture). It creates
// its own auth profile + pool to satisfy job_history's foreign keys.
func seedJobRetentionPath(t *testing.T, database *db.DB, runnerName, retentionPath string, completedAt time.Time) {
	t.Helper()
	ctx := context.Background()
	uniq := fmt.Sprintf("%d", seedJobRetentionPathSeq.Add(1))
	profile, err := database.CreateAuthProfile(ctx, db.CreateAuthProfileParams{
		Name:                "github-" + runnerName + "-" + uniq,
		AuthMethod:          "github_app",
		AppID:               sql.NullInt64{Int64: 12345, Valid: true},
		PrivateKeyEncrypted: sql.NullString{String: "enc_priv_key", Valid: true},
		TokenEncrypted:      sql.NullString{String: "enc_token", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateAuthProfile failed: %v", err)
	}
	pool, err := database.CreateRunnerPool(ctx, db.CreateRunnerPoolParams{
		Name:           "pool-" + runnerName + "-" + uniq,
		Provider:       "github",
		RepositoryUrl:  "https://github.com/org/repo",
		AuthProfileID:  profile.ID,
		Scope:          "repo",
		MinIdleRunners: 1,
		MaxConcurrency: 5,
	})
	if err != nil {
		t.Fatalf("CreateRunnerPool failed: %v", err)
	}
	_, err = database.CreateJobHistory(ctx, db.CreateJobHistoryParams{
		PoolID:           pool.ID,
		RunnerName:       runnerName,
		Status:           "success",
		CompletedAt:      sql.NullTime{Time: completedAt, Valid: true},
		LogRetentionPath: sql.NullString{String: retentionPath, Valid: retentionPath != ""},
		Source:           "webhook",
	})
	if err != nil {
		t.Fatalf("seeding job history for %q: %v", runnerName, err)
	}
}

func TestLatestCaptureRunnerID_ResolvesNewestCapture(t *testing.T) {
	database := setupTestDB(t)
	resolver := server.NewDBRunnerLogResolver(database)
	ctx := context.Background()

	base := time.Now().UTC().Add(-time.Hour)
	seedJobRetentionPath(t, database, "runnero-kraken-a308de",
		filepath.Join("/data", "logs", "old-container-id.log.jsonl.gz"), base)
	seedJobRetentionPath(t, database, "runnero-kraken-a308de",
		filepath.Join("/data", "logs", "new-container-id.log.jsonl.gz"), base.Add(10*time.Minute))

	got, err := resolver.LatestCaptureRunnerID(ctx, "runnero-kraken-a308de")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if got != "new-container-id" {
		t.Fatalf("resolved id = %q, want newest capture id %q", got, "new-container-id")
	}
}

func TestLatestCaptureRunnerID_NoCapture(t *testing.T) {
	database := setupTestDB(t)
	resolver := server.NewDBRunnerLogResolver(database)
	ctx := context.Background()

	if _, err := resolver.LatestCaptureRunnerID(ctx, "runnero-never-ran"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unknown runner: err = %v, want sql.ErrNoRows", err)
	}

	// Open row (no retention path yet): not a resolvable capture.
	seedJobRetentionPath(t, database, "runnero-open", "", time.Now().UTC())
	if _, err := resolver.LatestCaptureRunnerID(ctx, "runnero-open"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("open row: err = %v, want sql.ErrNoRows", err)
	}
}

func TestLatestCaptureRunnerID_RejectsMalformedStoredPaths(t *testing.T) {
	database := setupTestDB(t)
	resolver := server.NewDBRunnerLogResolver(database)
	ctx := context.Background()

	// Malformed ids are rejected outright.
	rejected := []struct {
		name string
		path string
	}{
		{"missing capture suffix", filepath.Join("/data", "logs", "plain-name.txt")},
		{"dotfile id", filepath.Join("/data", "logs", ".hidden.log.jsonl.gz")},
	}
	for _, tc := range rejected {
		seedJobRetentionPath(t, database, "runnero-"+tc.name, tc.path, time.Now().UTC())
		if _, err := resolver.LatestCaptureRunnerID(ctx, "runnero-"+tc.name); err == nil {
			t.Fatalf("%s: expected error for stored path %q", tc.name, tc.path)
		}
	}

	// Directory components (including "..") never survive: the stored path is
	// reduced to its base name, so a traversal-looking row can only resolve
	// to a plain validated id — never escape DATA_DIR/logs.
	seedJobRetentionPath(t, database, "runnero-traversal",
		filepath.Join("/data", "logs", "..", "passwd.log.jsonl.gz"), time.Now().UTC())
	got, err := resolver.LatestCaptureRunnerID(ctx, "runnero-traversal")
	if err != nil {
		t.Fatalf("traversal path should neutralize to a base-name id: %v", err)
	}
	if got != "passwd" {
		t.Fatalf("resolved id = %q, want neutralized %q", got, "passwd")
	}

	// A stale absolute prefix from another deployment resolves by base name too.
	seedJobRetentionPath(t, database, "runnero-sneaky",
		filepath.Join("/elsewhere", "sub", "dir", "escape-id.log.jsonl.gz"), time.Now().UTC())
	got, err = resolver.LatestCaptureRunnerID(ctx, "runnero-sneaky")
	if err != nil {
		t.Fatalf("stale-prefix path should resolve by base name: %v", err)
	}
	if got != "escape-id" {
		t.Fatalf("resolved id = %q, want %q", got, "escape-id")
	}
}

func TestLatestCaptureRunnerID_RejectsInvalidRunnerName(t *testing.T) {
	database := setupTestDB(t)
	resolver := server.NewDBRunnerLogResolver(database)

	if _, err := resolver.LatestCaptureRunnerID(context.Background(), "../etc/passwd"); err == nil {
		t.Fatal("expected error for path-like runner name")
	}
}

// writeCaptureFile writes a gzipped JSONL capture under
// DATA_DIR/logs/<id>.log.jsonl.gz, mirroring orchestrator.CaptureAndCompressLogs.
func writeCaptureFile(t *testing.T, dataDir, id string, contents []string) {
	t.Helper()
	logFile := filepath.Join(dataDir, "logs", id+".log.jsonl.gz")
	if err := os.MkdirAll(filepath.Dir(logFile), 0o755); err != nil {
		t.Fatalf("creating log dir: %v", err)
	}
	f, err := os.Create(logFile)
	if err != nil {
		t.Fatalf("creating log file: %v", err)
	}
	gz := gzip.NewWriter(f)
	type entry struct {
		Timestamp string `json:"timestamp"`
		Stream    string `json:"stream"`
		Content   string `json:"content"`
	}
	enc := json.NewEncoder(gz)
	for _, line := range contents {
		if err := enc.Encode(entry{Timestamp: "2026-09-20T10:00:00Z", Stream: "stdout", Content: line}); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	_ = gz.Close()
	_ = f.Close()
}

// TestGetRunnerLogs_ResolvesRunnerNameThroughHistory pins the RUN-252 read
// path end-to-end: captures are filed under the container ID, but callers key
// runners by container name; GetRunnerLogs must resolve the name through job
// history and serve the capture.
func TestGetRunnerLogs_ResolvesRunnerNameThroughHistory(t *testing.T) {
	ctx := context.Background()
	database := setupTestDB(t)
	dataDir := t.TempDir()

	const containerID = "a1b2c3d4e5f6a7b8a1b2c3d4e5f6a7b8a1b2c3d4e5f6a7b8a1b2c3d4e5f6a7b8"
	const runnerName = "runnero-kraken-a308de"
	writeCaptureFile(t, dataDir, containerID, []string{"Job setup complete", "Running step 1", "Exit code 0"})

	seedJobRetentionPath(t, database, runnerName,
		filepath.Join(dataDir, "logs", containerID+".log.jsonl.gz"), time.Now().UTC())

	srv := server.New(server.Options{
		Port:              8080,
		AuthDB:            database,
		DataDir:           dataDir,
		Session:           testSessionConfig(),
		RunnerLogResolver: server.NewDBRunnerLogResolver(database),
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	authClient := supervisorv1connect.NewAuthServiceClient(ts.Client(), ts.URL)
	_, _ = authClient.SetupAdmin(ctx, connect.NewRequest(&supervisorv1.SetupAdminRequest{Username: "admin", Password: "password123456"}))
	loginRes, _ := authClient.Login(ctx, connect.NewRequest(&supervisorv1.LoginRequest{Username: "admin", Password: "password123456"}))
	rawCookie := strings.Split(strings.Split(loginRes.Header().Get("Set-Cookie"), ";")[0], "=")[1]

	client := supervisorv1connect.NewLogServiceClient(ts.Client(), ts.URL)
	get := func(runnerRef string) ([]*supervisorv1.LogChunk, error) {
		req := connect.NewRequest(&supervisorv1.GetRunnerLogsRequest{RunnerId: runnerRef})
		req.Header().Set("Cookie", "session_token="+rawCookie)
		res, err := client.GetRunnerLogs(ctx, req)
		if err != nil {
			return nil, err
		}
		return res.Msg.Lines, nil
	}

	// By container NAME (what the history page sends): resolves through history.
	byName, err := get(runnerName)
	if err != nil {
		t.Fatalf("GetRunnerLogs by runner name failed: %v", err)
	}
	if len(byName) != 3 {
		t.Fatalf("by name: expected 3 chunks, got %d", len(byName))
	}

	// By container ID (historical contract): unchanged direct hit.
	byID, err := get(containerID)
	if err != nil {
		t.Fatalf("GetRunnerLogs by container id failed: %v", err)
	}
	if len(byID) != 3 {
		t.Fatalf("by id: expected 3 chunks, got %d", len(byID))
	}

	// Unknown runner with no history and no file: still NotFound.
	if _, err := get("runnero-never-existed"); err == nil {
		t.Fatal("expected NotFound for unknown runner")
	} else if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown runner: code = %v, want NotFound", connect.CodeOf(err))
	}

	// A name whose recorded capture file is gone must stay NotFound (not 500).
	seedJobRetentionPath(t, database, "runnero-missing-capture",
		filepath.Join(dataDir, "logs", "deadbeef-deleted.log.jsonl.gz"), time.Now().UTC())
	if _, err := get("runnero-missing-capture"); err == nil {
		t.Fatal("expected NotFound when resolved capture file is absent")
	}
}
