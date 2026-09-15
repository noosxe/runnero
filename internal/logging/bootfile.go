package logging

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// BootFileSink mirrors the supervisor's structured log stream into
// boot-separated files under <data-dir>/logs/supervisor/ (RUN-186,
// docs/28 §5.1), in addition to stdout. It is a plain io.Writer: the
// built-in slog handler formats records exactly as it does for stdout,
// so the persisted stream is byte-identical ndjson.
//
// Supervisor recreation may happen at any time, so the sink keeps no
// state that survives a crash being authoritative: each boot writes its
// own boot-<unix-ts>-<bootid>.ndjson file (immutable once the boot
// ends), opens in append mode, and rotates purely by size. A hard
// crash may lose the last buffered lines; the DB remains the source of
// truth for state.
type BootFileSink struct {
	mu       sync.Mutex
	dir      string
	base     string // "boot-<unix-ts>-<bootid>" without extension
	maxBytes int64

	f    *os.File
	size int64
	seq  int // rotation counter; 0 = the base file
}

// OpenBootFileSink creates the boot log file for this supervisor
// lifetime and writes a self-describing header line as its first
// record. maxBytes is the per-file rotation threshold; values < 1
// disable rotation (the boot file grows unbounded).
func OpenBootFileSink(dataDir, bootID string, startedAt time.Time, version string, maxBytes int64) (*BootFileSink, error) {
	if dataDir == "" {
		return nil, errors.New("boot log sink: data directory must not be empty")
	}
	if bootID == "" {
		return nil, errors.New("boot log sink: boot id must not be empty")
	}
	if len(bootID) > 8 {
		bootID = bootID[:8]
	}
	dir := filepath.Join(dataDir, "logs", "supervisor")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("boot log sink: creating %s: %w", dir, err)
	}

	s := &BootFileSink{
		dir:      dir,
		base:     fmt.Sprintf("boot-%d-%s", startedAt.Unix(), bootID),
		maxBytes: maxBytes,
	}
	if err := s.open(); err != nil {
		return nil, err
	}
	// Self-orientation header: which boot produced this file (docs/28 §4).
	header := fmt.Sprintf(`{"log":"runnero-supervisor","boot_id":%q,"started_at":%q,"version":%q}`+"\n",
		bootID, startedAt.UTC().Format(time.RFC3339Nano), version)
	if _, err := s.Write([]byte(header)); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("boot log sink: writing header: %w", err)
	}
	return s, nil
}

// Path returns the file currently appended to (the newest rotation).
func (s *BootFileSink) Path() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return filepath.Join(s.dir, s.base+rotationSuffix(s.seq)+".ndjson")
}

// CurrentBootFile reports the base name of the file being appended to
// (docs/29 §5.2 server.BootLogFile seam). Nil-safe: a nil sink answers "".
func (s *BootFileSink) CurrentBootFile() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.base + rotationSuffix(s.seq) + ".ndjson"
}

func rotationSuffix(seq int) string {
	if seq == 0 {
		return ""
	}
	return fmt.Sprintf("-%04d", seq)
}

func (s *BootFileSink) open() error {
	name := filepath.Join(s.dir, s.base+rotationSuffix(s.seq)+".ndjson")
	f, err := os.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("boot log sink: opening %s: %w", name, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("boot log sink: stat %s: %w", name, err)
	}
	s.f = f
	s.size = info.Size()
	return nil
}

// Write appends p to the current boot file, rotating when the next
// record would push the file past the rotation threshold. Safe for
// concurrent use (slog handlers fan out from many goroutines).
func (s *BootFileSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return 0, errors.New("boot log sink: closed")
	}
	if s.maxBytes > 0 && s.size+int64(len(p)) > s.maxBytes {
		if err := s.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := s.f.Write(p)
	s.size += int64(n)
	return n, err
}

func (s *BootFileSink) rotate() error {
	if err := s.f.Close(); err != nil {
		return fmt.Errorf("boot log sink: closing rotated file: %w", err)
	}
	s.seq++
	return s.open()
}

// Close flushes nothing (writes are unbuffered) and releases the file.
func (s *BootFileSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return nil
	}
	err := s.f.Close()
	s.f = nil
	return err
}

// SweepTarget is one directory whose file count is retained under
// MaxFiles (oldest modification time removed first). MaxFiles < 1 means
// the directory participates only in the global budget.
type SweepTarget struct {
	Dir      string
	MaxFiles int
}

// SweepLogs enforces durable-log retention (RUN-186, docs/28 §5.5):
// per-directory file counts first, then the total budget across every
// target, both oldest-modification-time first. Missing directories are
// skipped; removal errors on individual files are ignored (best-effort
// sweep, reported via the returned count of removed files). It returns
// the number of files removed.
func SweepLogs(targets []SweepTarget, budgetBytes int64) int {
	type entry struct {
		path string
		size int64
		mod  time.Time
	}
	remaining := make([]entry, 0, 64)
	removed := 0

	for _, t := range targets {
		files, err := os.ReadDir(t.Dir)
		if err != nil {
			continue // nothing retained here yet
		}
		var entries []entry
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			info, err := f.Info()
			if err != nil {
				continue
			}
			entries = append(entries, entry{path: filepath.Join(t.Dir, f.Name()), size: info.Size(), mod: info.ModTime()})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].mod.Before(entries[j].mod) })
		if t.MaxFiles > 0 && len(entries) > t.MaxFiles {
			for _, e := range entries[:len(entries)-t.MaxFiles] {
				if os.Remove(e.path) == nil {
					removed++
				}
			}
			entries = entries[len(entries)-t.MaxFiles:]
		}
		remaining = append(remaining, entries...)
	}

	if budgetBytes <= 0 {
		return removed
	}
	var total int64
	for _, e := range remaining {
		total += e.size
	}
	if total <= budgetBytes {
		return removed
	}
	sort.Slice(remaining, func(i, j int) bool { return remaining[i].mod.Before(remaining[j].mod) })
	for _, e := range remaining {
		if total <= budgetBytes {
			break
		}
		if os.Remove(e.path) == nil {
			total -= e.size
			removed++
		}
	}
	return removed
}

// compile-time check that the sink satisfies the writer seam used by
// logging.Setup.
var _ io.Writer = (*BootFileSink)(nil)
