package orchestrator_test

import (
	"bytes"
	"encoding/binary"
	"io"
	"path/filepath"
	"testing"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/noosxe/runnero/internal/orchestrator"
)

func writeFrame(w io.Writer, stream stdcopy.StdType, data []byte) {
	var header [8]byte
	header[0] = byte(stream)
	binary.BigEndian.PutUint32(header[4:], uint32(len(data)))
	_, _ = w.Write(header[:])
	_, _ = w.Write(data)
}

func TestCaptureAndCompressLogs_MultiplexedStream(t *testing.T) {
	tempDir := t.TempDir()
	destPath := orchestrator.LogPath(tempDir, "runner-test-123")

	// Construct multiplexed stream using Docker frame format
	var rawBuffer bytes.Buffer
	writeFrame(&rawBuffer, stdcopy.Stdout, []byte("2026-09-03T10:00:01.000000000Z Starting runner process\n"))
	writeFrame(&rawBuffer, stdcopy.Stderr, []byte("2026-09-03T10:00:02.000000000Z Warning: high memory usage\n"))
	writeFrame(&rawBuffer, stdcopy.Stdout, []byte("2026-09-03T10:00:03.000000000Z Job succeeded\n"))

	count, err := orchestrator.CaptureAndCompressLogs(&rawBuffer, destPath)
	if err != nil {
		t.Fatalf("CaptureAndCompressLogs failed: %v", err)
	}
	if count != 3 {
		t.Fatalf("expected 3 entries captured, got %d", count)
	}

	// Decompress and verify
	entries, err := orchestrator.ReadGzippedJSONLLogs(destPath)
	if err != nil {
		t.Fatalf("ReadGzippedJSONLLogs failed: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries read back, got %d", len(entries))
	}

	// Line 1: stdout
	if entries[0].Stream != "stdout" || entries[0].Content != "Starting runner process" {
		t.Errorf("unexpected entry 0: %+v", entries[0])
	}
	if entries[0].Timestamp != "2026-09-03T10:00:01Z" {
		t.Errorf("unexpected timestamp 0: %q", entries[0].Timestamp)
	}

	// Line 2: stderr
	if entries[1].Stream != "stderr" || entries[1].Content != "Warning: high memory usage" {
		t.Errorf("unexpected entry 1: %+v", entries[1])
	}
	if entries[1].Timestamp != "2026-09-03T10:00:02Z" {
		t.Errorf("unexpected timestamp 1: %q", entries[1].Timestamp)
	}

	// Line 3: stdout
	if entries[2].Stream != "stdout" || entries[2].Content != "Job succeeded" {
		t.Errorf("unexpected entry 2: %+v", entries[2])
	}
	if entries[2].Timestamp != "2026-09-03T10:00:03Z" {
		t.Errorf("unexpected timestamp 2: %q", entries[2].Timestamp)
	}
}

func TestCaptureAndCompressLogs_PlainTextFallback(t *testing.T) {
	tempDir := t.TempDir()
	destPath := filepath.Join(tempDir, "plain.log.jsonl.gz")

	plainData := "Plain log line 1\nPlain log line 2\n"
	count, err := orchestrator.CaptureAndCompressLogs(bytes.NewBufferString(plainData), destPath)
	if err != nil {
		t.Fatalf("CaptureAndCompressLogs plain failed: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 entries, got %d", count)
	}

	entries, err := orchestrator.ReadGzippedJSONLLogs(destPath)
	if err != nil {
		t.Fatalf("ReadGzippedJSONLLogs failed: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Stream != "stdout" || entries[0].Content != "Plain log line 1" {
		t.Errorf("unexpected plain entry 0: %+v", entries[0])
	}
	if entries[1].Stream != "stdout" || entries[1].Content != "Plain log line 2" {
		t.Errorf("unexpected plain entry 1: %+v", entries[1])
	}
}

func TestLogPath(t *testing.T) {
	got := orchestrator.LogPath("/var/data", "runner-abc-123")
	want := filepath.Join("/var/data", "logs", "runner-abc-123.log.jsonl.gz")
	if got != want {
		t.Errorf("LogPath = %q, want %q", got, want)
	}
}
