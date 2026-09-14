package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Removal reasons for the structured removal record (RUN-186,
// docs/28 §5.4). Every runner-container termination path tags the
// record with exactly one of these so post-mortems can answer "why was
// runner X removed" by grepping a single file.
const (
	RemovalReasonReap          = "reap"           // die/destroy event or audit-cycle reap of an exited runner
	RemovalReasonTaskExit      = "task-exit"      // one-off task container (e.g. Renovate bot)
	RemovalReasonLifetimeLimit = "lifetime-limit" // busy runner exceeded max_runner_lifetime_seconds
	RemovalReasonIdleDrain     = "idle-drain"     // scale-down of a standby runner
	RemovalReasonShutdown      = "shutdown"       // graceful/immediate supervisor shutdown
	RemovalReasonPoolDrain     = "pool-drain"     // pool deletion cleanup
	RemovalReasonRecycle       = "recycle"        // idle recycle after a spawn-identity change
	RemovalReasonManual        = "manual"         // operator-initiated termination via API/UI
	RemovalReasonCreateFailure = "create-failure" // container start failed; nothing to capture
)

// RemovalCapture records the outcome of the best-effort stdout capture
// that precedes every removal: either where the capture landed, or why
// it produced nothing. Bytes is filled in afterwards by stat-ing the
// capture file.
type RemovalCapture struct {
	OK      bool   `json:"ok"`
	Bytes   int64  `json:"bytes,omitempty"`
	File    string `json:"file,omitempty"`
	Error   string `json:"error,omitempty"`
	Skipped string `json:"skipped,omitempty"`
}

// RemovalRecord is one line of removals.jsonl (docs/28 §5.4): the
// durable, greppable answer to "why was runner X removed", including
// the provider busy flag and veto outcome the RUN-182-class
// investigations previously had to recover from ephemeral logs.
type RemovalRecord struct {
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
	Capture      RemovalCapture `json:"capture"`
}

// RemovalLogger appends removal records to
// <data-dir>/logs/removals.jsonl (docs/28 §4). All methods are nil-safe
// and best-effort: persistence is a forensic aid, never a lifecycle
// gate — a failure to append is logged and swallowed.
type RemovalLogger struct {
	mu     sync.Mutex
	path   string
	bootID string
}

// NewRemovalLogger builds the appender for dataDir. A nil *RemovalLogger
// (or empty dataDir) disables record emission cleanly.
func NewRemovalLogger(dataDir, bootID string) *RemovalLogger {
	if dataDir == "" {
		return nil
	}
	return &RemovalLogger{
		path:   filepath.Join(dataDir, "logs", "removals.jsonl"),
		bootID: bootID,
	}
}

// Enabled reports whether records are being persisted.
func (l *RemovalLogger) Enabled() bool { return l != nil }

// Record appends one line. Timestamp and boot id are filled in if
// unset. The file is opened per record in append mode: removals are
// rare events, and a crash must never leave a truncated line behind.
func (l *RemovalLogger) Record(rec RemovalRecord) {
	if l == nil {
		return
	}
	if rec.TS.IsZero() {
		rec.TS = time.Now().UTC()
	}
	if rec.BootID == "" {
		rec.BootID = l.bootID
	}
	line, err := json.Marshal(rec)
	if err != nil {
		logger.Warn("marshaling removal record", "runner", rec.RunnerID, "err", err)
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		logger.Warn("creating removal log directory", "err", err)
		return
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		logger.Warn("opening removal log", "err", err)
		return
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(append(line, '\n')); err != nil {
		logger.Warn("appending removal record", "runner", rec.RunnerID, "err", err)
	}
}
