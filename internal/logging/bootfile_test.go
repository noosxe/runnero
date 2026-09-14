package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBootFileSink_WritesHeaderAndAppends(t *testing.T) {
	dir := t.TempDir()
	started := time.Now().UTC()

	sink, err := OpenBootFileSink(dir, "abcdef1234567890", started, "test-version", 0)
	if err != nil {
		t.Fatalf("opening sink: %v", err)
	}

	if _, err := sink.Write([]byte(`{"msg":"one"}` + "\n")); err != nil {
		t.Fatalf("write one: %v", err)
	}
	if _, err := sink.Write([]byte(`{"msg":"two"}` + "\n")); err != nil {
		t.Fatalf("write two: %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	files, err := os.ReadDir(filepath.Join(dir, "logs", "supervisor"))
	if err != nil {
		t.Fatalf("reading supervisor dir: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("want 1 boot file, got %d", len(files))
	}
	if !strings.HasPrefix(files[0].Name(), "boot-") || !strings.HasSuffix(files[0].Name(), "-abcdef12.ndjson") {
		t.Fatalf("unexpected boot file name %q", files[0].Name())
	}

	data, err := os.ReadFile(filepath.Join(dir, "logs", "supervisor", files[0].Name()))
	if err != nil {
		t.Fatalf("reading boot file: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 lines (header + 2 records), got %d:\n%s", len(lines), data)
	}
	if !strings.Contains(lines[0], `"boot_id":"abcdef12"`) || !strings.Contains(lines[0], `"version":"test-version"`) {
		t.Fatalf("header line missing boot id/version: %s", lines[0])
	}
	if lines[1] != `{"msg":"one"}` || lines[2] != `{"msg":"two"}` {
		t.Fatalf("records corrupted: %q %q", lines[1], lines[2])
	}
}

func TestBootFileSink_RotatesAtThreshold(t *testing.T) {
	dir := t.TempDir()
	// Small threshold: header (~90 bytes) + one record fit, the next rotates.
	sink, err := OpenBootFileSink(dir, "bootid", time.Now().UTC(), "v", 160)
	if err != nil {
		t.Fatalf("opening sink: %v", err)
	}
	record := `{"msg":"x"}` + "\n"
	for i := 0; i < 5; i++ {
		if _, err := sink.Write([]byte(record)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	first := sink.Path()
	if err := sink.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	files, err := os.ReadDir(filepath.Join(dir, "logs", "supervisor"))
	if err != nil {
		t.Fatalf("reading supervisor dir: %v", err)
	}
	if len(files) < 2 {
		t.Fatalf("rotation did not happen: %d files", len(files))
	}
	if !strings.HasSuffix(first, "-0001.ndjson") {
		t.Fatalf("current rotation file %q lacks -0001 suffix", first)
	}
}

func TestBootFileSink_ConcurrentWritesStayWhole(t *testing.T) {
	dir := t.TempDir()
	sink, err := OpenBootFileSink(dir, "bootid", time.Now().UTC(), "v", 0)
	if err != nil {
		t.Fatalf("opening sink: %v", err)
	}
	record := []byte(strings.Repeat("x", 200) + "\n")
	done := make(chan struct{})
	for w := 0; w < 8; w++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for i := 0; i < 50; i++ {
				if _, err := sink.Write(record); err != nil {
					t.Errorf("write: %v", err)
					return
				}
			}
		}()
	}
	for w := 0; w < 8; w++ {
		<-done
	}
	_ = sink.Close()

	data, err := os.ReadFile(sink.Path())
	if err != nil {
		t.Fatalf("reading sink: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 8*50+1 {
		t.Fatalf("want %d lines (header + records), got %d — torn writes?", 8*50+1, len(lines))
	}
}

func TestSweepLogs_FileCountAndBudget(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string, size int64) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, make([]byte, size), 0o600); err != nil {
			t.Fatalf("creating %s: %v", name, err)
		}
		return p
	}
	// Deterministic aging: a, b, c oldest to newest.
	old := time.Now().Add(-3 * time.Hour)
	for _, name := range []string{"a.log", "b.log", "c.log"} {
		p := mk(name, 100)
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}
	mk("d.log", 100) // newest

	// Count cap: 4 files, keep 2 newest → a and b (oldest) are removed.
	removed := SweepLogs([]SweepTarget{{Dir: dir, MaxFiles: 2}}, 0)
	if removed != 2 {
		t.Fatalf("want 2 files removed by count cap, got %d", removed)
	}
	for _, gone := range []string{"a.log", "b.log"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Fatalf("%s should have been removed", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "c.log")); err != nil {
		t.Fatalf("c.log should have survived")
	}
	// Budget cap: c+d = 200 bytes; a 150-byte budget drops c (older).
	removed = SweepLogs([]SweepTarget{{Dir: dir, MaxFiles: 0}}, 150)
	if removed != 1 {
		t.Fatalf("want 1 file removed by budget, got %d", removed)
	}
	if _, err := os.Stat(filepath.Join(dir, "c.log")); !os.IsNotExist(err) {
		t.Fatalf("c.log should have been removed by budget")
	}
	if _, err := os.Stat(filepath.Join(dir, "d.log")); err != nil {
		t.Fatalf("d.log should have survived")
	}

	// Missing directories are skipped, not errors.
	if n := SweepLogs([]SweepTarget{{Dir: filepath.Join(dir, "nope"), MaxFiles: 5}}, 0); n != 0 {
		t.Fatalf("missing dir sweep removed %d files", n)
	}
}
