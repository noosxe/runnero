package orchestrator_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/noosxe/runnero/internal/db"
	"github.com/noosxe/runnero/internal/orchestrator"
	"github.com/noosxe/runnero/internal/provider"
)

// Tests for the RUN-252 fix: job_history rows must carry the capture path so
// the UI's name-keyed log lookups can find the archived runner stdout.

// closeCaptureHarness mirrors newEnrichHarness but with a DataDir and a
// CaptureLogs hook, driving the busy->idle flip through Reconcile.
type closeCaptureHarness struct {
	busySyncHarness
	prov        *jobsListerProvider
	rec         *mockJobRecorder
	dataDir     string
	capturePath string
	captureErr  error
	mu          sync.Mutex
	capturedIDs []string
}

func newCloseCaptureHarness(t *testing.T, pool db.RunnerPool) *closeCaptureHarness {
	t.Helper()
	h := &closeCaptureHarness{
		busySyncHarness: busySyncHarness{
			mockProv:    &mockGitProvider{},
			liveRunners: make(map[string]orchestrator.RunnerStatus),
		},
	}
	h.dataDir = t.TempDir()
	h.capturePath = filepath.Join(h.dataDir, "logs", "cap.log.jsonl.gz")
	h.prov = &jobsListerProvider{mockGitProvider: h.mockProv}
	h.rec = &mockJobRecorder{}
	resolver := &mockGitProviderResolver{
		providers: map[int64]provider.GitProvider{10: h.prov},
	}
	h.engine = &orchestrator.MockContainerProvider{
		AuditRunnersFn: func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
			h.liveMu.Lock()
			defer h.liveMu.Unlock()
			res := make([]orchestrator.RunnerStatus, 0, len(h.liveRunners))
			for _, r := range h.liveRunners {
				res = append(res, r)
			}
			return res, nil
		},
		SpawnRunnerFn: func(ctx context.Context, cfg orchestrator.RunnerConfig) (string, error) {
			return "container-" + cfg.Name, nil
		},
		PingFn: func(ctx context.Context) error { return nil },
		CaptureLogsFn: func(ctx context.Context, containerID, dataDir string) (string, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.capturedIDs = append(h.capturedIDs, containerID)
			if h.captureErr != nil {
				return "", h.captureErr
			}
			return h.capturePath, nil
		},
	}
	h.reconciler = orchestrator.NewReconciler(h.engine)
	h.ctrl = orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:               &mockPoolRepo{pools: []db.RunnerPool{pool}},
		ContainerEngine:  h.engine,
		ProviderResolver: resolver,
		Reconciler:       h.reconciler,
		JobRecorder:      h.rec,
		DataDir:          h.dataDir,
		Interval:         time.Hour,
	})
	if err := h.ctrl.Boot(context.Background()); err != nil {
		t.Fatalf("boot failed: %v", err)
	}
	return h
}

// TestJobClose_CapturesLogsOnIdleFlip pins the warm-pool mainline (RUN-252):
// on the busy->idle flip the runner keeps living, so the close must snapshot
// the container logs and record the capture path on the job row — otherwise
// no capture exists for the job until the runner's eventual removal.
func TestJobClose_CapturesLogsOnIdleFlip(t *testing.T) {
	ctx := context.Background()
	pool := busySyncPool("close-capture", 0, 5)
	h := newCloseCaptureHarness(t, pool)
	h.injectRunner(pool, "runnero-close-1", false)

	h.mockProv.remoteRunners = []provider.RemoteRunnerStatus{
		{ID: 7101, Name: "runnero-close-1", Busy: true, Online: true},
	}
	if err := h.ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile (busy) failed: %v", err)
	}
	h.mockProv.remoteRunners = []provider.RemoteRunnerStatus{
		{ID: 7101, Name: "runnero-close-1", Busy: false, Online: true},
	}
	if err := h.ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile (idle) failed: %v", err)
	}

	if len(h.rec.closes) != 1 {
		t.Fatalf("expected 1 close, got %d", len(h.rec.closes))
	}
	if h.rec.closes[0].logPath != h.capturePath {
		t.Fatalf("close logPath = %q, want captured %q", h.rec.closes[0].logPath, h.capturePath)
	}
	if len(h.capturedIDs) == 0 || h.capturedIDs[0] != "container-runnero-close-1" {
		t.Fatalf("capture ran for %v, want the tracked container id", h.capturedIDs)
	}
}

// TestJobClose_CaptureFailureClosesRowWithoutPath: a failed close-time
// capture must not block the close (docs/21 G3) — the row closes with an
// empty path and the removal-time capture / resolver fallback cover it later.
func TestJobClose_CaptureFailureClosesRowWithoutPath(t *testing.T) {
	ctx := context.Background()
	pool := busySyncPool("close-capture-fail", 0, 5)
	h := newCloseCaptureHarness(t, pool)
	h.captureErr = errors.New("daemon error")
	h.injectRunner(pool, "runnero-close-2", false)

	h.mockProv.remoteRunners = []provider.RemoteRunnerStatus{
		{ID: 7102, Name: "runnero-close-2", Busy: true, Online: true},
	}
	if err := h.ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile (busy) failed: %v", err)
	}
	h.mockProv.remoteRunners = []provider.RemoteRunnerStatus{
		{ID: 7102, Name: "runnero-close-2", Busy: false, Online: true},
	}
	if err := h.ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile (idle) failed: %v", err)
	}

	if len(h.rec.closes) != 1 {
		t.Fatalf("expected 1 close, got %d", len(h.rec.closes))
	}
	if h.rec.closes[0].logPath != "" {
		t.Fatalf("close logPath = %q, want empty on capture failure", h.rec.closes[0].logPath)
	}
}

// TestReapContainer_ThreadsCapturePathIntoJobClose: on a genuine die event
// the capture runs inside terminateAndRecord and the returned path lands on
// the closed job row (RUN-252) — the row is closed after the capture.
func TestReapContainer_ThreadsCapturePathIntoJobClose(t *testing.T) {
	ctx := context.Background()
	pool := busySyncPool("reap-capture", 0, 5)
	h := newCloseCaptureHarness(t, pool)

	// Track a busy runner so closeJobRowOnDeath finds an open row by name.
	h.reconciler.TrackRunner(orchestrator.RunnerStatus{
		PoolID:    pool.ID,
		ID:        "dead-container-1",
		Name:      "runnero-reap-1",
		PoolName:  pool.Name,
		State:     "running",
		IsBusy:    true,
		SpawnedAt: time.Now().UTC(),
	})

	if err := h.ctrl.HandleContainerEvent(ctx, orchestrator.ContainerEvent{
		ContainerID: "dead-container-1",
		PoolName:    pool.Name,
		PoolID:      pool.ID,
		Action:      "die",
		ExitCode:    0,
		Timestamp:   time.Now().UTC(),
	}); err != nil {
		t.Fatalf("die event: %v", err)
	}

	if len(h.rec.closes) != 1 {
		t.Fatalf("expected 1 close, got %d", len(h.rec.closes))
	}
	if h.rec.closes[0].status != "completed" {
		t.Fatalf("clean exit must close as completed, got %q", h.rec.closes[0].status)
	}
	if h.rec.closes[0].logPath != h.capturePath {
		t.Fatalf("close logPath = %q, want captured %q", h.rec.closes[0].logPath, h.capturePath)
	}
}
