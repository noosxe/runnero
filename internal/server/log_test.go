package server_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	supervisorv1 "github.com/noosxe/runnero/internal/pb/supervisor/v1"
	"github.com/noosxe/runnero/internal/pb/supervisor/v1/supervisorv1connect"
	"github.com/noosxe/runnero/internal/server"
)

type mockLogStreamer struct {
	streamFn func(ctx context.Context, containerID string) (io.ReadCloser, error)
}

func (m *mockLogStreamer) StreamLogs(ctx context.Context, containerID string) (io.ReadCloser, error) {
	if m.streamFn != nil {
		return m.streamFn(ctx, containerID)
	}
	return io.NopCloser(strings.NewReader("")), nil
}

// buildDockerFrame creates an 8-byte multiplexed header + payload per Docker standard.
func buildDockerFrame(streamType byte, message string) []byte {
	buf := new(bytes.Buffer)
	header := make([]byte, 8)
	header[0] = streamType // 1=stdout, 2=stderr
	binary.BigEndian.PutUint32(header[4:8], uint32(len(message)))
	buf.Write(header)
	buf.WriteString(message)
	return buf.Bytes()
}

func TestStreamRunnerLogs_DockerMultiplexed(t *testing.T) {
	ctx := context.Background()
	database, jwtSecret := setupTestDB(t)

	// Construct Docker multiplexed stream: stdout line then stderr line
	tsStr := "2026-09-03T10:00:00.000000000Z"
	msgStdout := fmt.Sprintf("%s Runner listening for jobs...\n", tsStr)
	msgStderr := fmt.Sprintf("%s Warning: high memory usage\n", tsStr)

	streamBuf := new(bytes.Buffer)
	streamBuf.Write(buildDockerFrame(1, msgStdout))
	streamBuf.Write(buildDockerFrame(2, msgStderr))

	streamer := &mockLogStreamer{
		streamFn: func(ctx context.Context, containerID string) (io.ReadCloser, error) {
			if containerID != "test-runner-1" {
				return nil, errors.New("container not found")
			}
			return io.NopCloser(bytes.NewReader(streamBuf.Bytes())), nil
		},
	}

	dataDir := t.TempDir()
	srv := server.New(server.Options{
		Port:             8080,
		AuthDB:           database,
		DataDir:          dataDir,
		LogStreamer:      streamer,
		JWTSigningSecret: jwtSecret,
	})

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Authenticate
	authClient := supervisorv1connect.NewAuthServiceClient(ts.Client(), ts.URL)
	_, err := authClient.SetupAdmin(ctx, connect.NewRequest(&supervisorv1.SetupAdminRequest{
		Username: "admin",
		Password: "password123",
	}))
	if err != nil {
		t.Fatalf("SetupAdmin failed: %v", err)
	}

	loginRes, err := authClient.Login(ctx, connect.NewRequest(&supervisorv1.LoginRequest{
		Username: "admin",
		Password: "password123",
	}))
	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}
	cookie := loginRes.Header().Get("Set-Cookie")
	rawCookie := strings.Split(strings.Split(cookie, ";")[0], "=")[1]

	client := supervisorv1connect.NewLogServiceClient(ts.Client(), ts.URL)

	// Stream logs
	streamReq := connect.NewRequest(&supervisorv1.StreamRunnerLogsRequest{
		RunnerId: "test-runner-1",
	})
	streamReq.Header().Set("Cookie", "session_token="+rawCookie)

	stream, err := client.StreamRunnerLogs(ctx, streamReq)
	if err != nil {
		t.Fatalf("StreamRunnerLogs failed: %v", err)
	}
	defer func() { _ = stream.Close() }()

	var received []*supervisorv1.LogChunk
	for stream.Receive() {
		received = append(received, stream.Msg())
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("stream ended with error: %v", err)
	}

	if len(received) != 2 {
		t.Fatalf("expected 2 chunks, got: %d", len(received))
	}

	if received[0].Stream != "stdout" || received[0].Content != "Runner listening for jobs..." {
		t.Errorf("chunk 0 mismatch: %+v", received[0])
	}
	if received[1].Stream != "stderr" || received[1].Content != "Warning: high memory usage" {
		t.Errorf("chunk 1 mismatch: %+v", received[1])
	}
}

func TestStreamRunnerLogs_ClientCancellation(t *testing.T) {
	ctx := context.Background()
	database, jwtSecret := setupTestDB(t)

	// Pipe to simulate an endless follow stream
	pipeR, pipeW := io.Pipe()
	go func() {
		_, _ = pipeW.Write(buildDockerFrame(1, "2026-09-03T10:00:00Z runner started\n"))
	}()

	streamer := &mockLogStreamer{
		streamFn: func(ctx context.Context, containerID string) (io.ReadCloser, error) {
			return pipeR, nil
		},
	}

	srv := server.New(server.Options{
		Port:             8080,
		AuthDB:           database,
		LogStreamer:      streamer,
		JWTSigningSecret: jwtSecret,
	})

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	authClient := supervisorv1connect.NewAuthServiceClient(ts.Client(), ts.URL)
	_, _ = authClient.SetupAdmin(ctx, connect.NewRequest(&supervisorv1.SetupAdminRequest{Username: "admin", Password: "password123"}))
	loginRes, _ := authClient.Login(ctx, connect.NewRequest(&supervisorv1.LoginRequest{Username: "admin", Password: "password123"}))
	rawCookie := strings.Split(strings.Split(loginRes.Header().Get("Set-Cookie"), ";")[0], "=")[1]

	client := supervisorv1connect.NewLogServiceClient(ts.Client(), ts.URL)

	cancelCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	streamReq := connect.NewRequest(&supervisorv1.StreamRunnerLogsRequest{
		RunnerId: "endless-runner",
	})
	streamReq.Header().Set("Cookie", "session_token="+rawCookie)

	stream, err := client.StreamRunnerLogs(cancelCtx, streamReq)
	if err != nil {
		t.Fatalf("StreamRunnerLogs failed: %v", err)
	}
	defer func() { _ = stream.Close() }()

	// First chunk should arrive
	if !stream.Receive() {
		t.Fatalf("expected initial chunk, got err: %v", stream.Err())
	}

	// Cancel context from client
	cancel()

	// Drain stream until it terminates
	for stream.Receive() {
	}
	_ = pipeW.Close()
	// Clean exit confirmed
}

func TestGetRunnerLogs_Historical(t *testing.T) {
	ctx := context.Background()
	database, jwtSecret := setupTestDB(t)
	dataDir := t.TempDir()

	// Create test gzipped JSONL log file: DATA_DIR/logs/runner-done.log.jsonl.gz
	runnerID := "runner-done"
	logFile := filepath.Join(dataDir, "logs", fmt.Sprintf("%s.log.jsonl.gz", runnerID))
	if err := os.MkdirAll(filepath.Dir(logFile), 0o755); err != nil {
		t.Fatalf("creating log dir: %v", err)
	}

	f, err := os.Create(logFile)
	if err != nil {
		t.Fatalf("creating log file: %v", err)
	}
	gz := gzip.NewWriter(f)
	type fileLogEntry struct {
		Timestamp string `json:"timestamp"`
		Stream    string `json:"stream"`
		Content   string `json:"content"`
	}
	entries := []fileLogEntry{
		{Timestamp: "2026-09-03T10:00:00Z", Stream: "stdout", Content: "Job setup complete"},
		{Timestamp: "2026-09-03T10:01:00Z", Stream: "stdout", Content: "Running step 1"},
		{Timestamp: "2026-09-03T10:02:00Z", Stream: "stderr", Content: "Exit code 0"},
	}
	enc := json.NewEncoder(gz)
	for _, e := range entries {
		if err := enc.Encode(e); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	_ = gz.Close()
	_ = f.Close()

	srv := server.New(server.Options{
		Port:             8080,
		AuthDB:           database,
		DataDir:          dataDir,
		JWTSigningSecret: jwtSecret,
	})

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	authClient := supervisorv1connect.NewAuthServiceClient(ts.Client(), ts.URL)
	_, _ = authClient.SetupAdmin(ctx, connect.NewRequest(&supervisorv1.SetupAdminRequest{Username: "admin", Password: "password123"}))
	loginRes, _ := authClient.Login(ctx, connect.NewRequest(&supervisorv1.LoginRequest{Username: "admin", Password: "password123"}))
	rawCookie := strings.Split(strings.Split(loginRes.Header().Get("Set-Cookie"), ";")[0], "=")[1]

	client := supervisorv1connect.NewLogServiceClient(ts.Client(), ts.URL)

	// 1. Get existing historical logs
	req := connect.NewRequest(&supervisorv1.GetRunnerLogsRequest{
		RunnerId: runnerID,
	})
	req.Header().Set("Cookie", "session_token="+rawCookie)
	res, err := client.GetRunnerLogs(ctx, req)
	if err != nil {
		t.Fatalf("GetRunnerLogs failed: %v", err)
	}

	if len(res.Msg.Lines) != 3 {
		t.Fatalf("expected 3 chunks, got: %d", len(res.Msg.Lines))
	}
	if res.Msg.Lines[0].Content != "Job setup complete" {
		t.Errorf("chunk 0 mismatch: %+v", res.Msg.Lines[0])
	}
	if res.Msg.Lines[2].Stream != "stderr" || res.Msg.Lines[2].Content != "Exit code 0" {
		t.Errorf("chunk 2 mismatch: %+v", res.Msg.Lines[2])
	}

	// 2. Non-existent runner logs return CodeNotFound
	badReq := connect.NewRequest(&supervisorv1.GetRunnerLogsRequest{
		RunnerId: "non-existent-runner",
	})
	badReq.Header().Set("Cookie", "session_token="+rawCookie)
	_, err = client.GetRunnerLogs(ctx, badReq)
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("non-existent runner want CodeNotFound, got: %v", err)
	}
}
