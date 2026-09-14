package orchestrator_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/noosxe/runnero/internal/db"
	"github.com/noosxe/runnero/internal/orchestrator"
)

// removalHarness wires a PoolController against a mock engine plus a
// RemovalLogger over a temp data dir, to verify the RUN-186 choke point
// emits exactly one structured record per termination path.
type removalHarness struct {
	engine      *orchestrator.MockContainerProvider
	reconciler  *orchestrator.Reconciler
	ctrl        *orchestrator.PoolController
	dataDir     string
	mu          sync.Mutex
	terminated  []string
	captureErr  error // when set, CaptureLogs fails with this error
	capturePath string
}

func newRemovalHarness(t *testing.T) *removalHarness {
	t.Helper()
	dataDir := t.TempDir()
	h := &removalHarness{engine: &orchestrator.MockContainerProvider{}, dataDir: dataDir}
	h.capturePath = filepath.Join(dataDir, "logs", "captured.log.jsonl.gz")
	if err := os.MkdirAll(filepath.Dir(h.capturePath), 0o700); err != nil {
		t.Fatalf("creating logs dir: %v", err)
	}
	if err := os.WriteFile(h.capturePath, []byte("captured output"), 0o600); err != nil {
		t.Fatalf("creating capture file: %v", err)
	}

	h.engine.CaptureLogsFn = func(ctx context.Context, containerID, dataDir string) (string, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.captureErr != nil {
			return "", h.captureErr
		}
		return h.capturePath, nil
	}
	h.engine.TerminateRunnerFn = func(ctx context.Context, containerID string) error {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.terminated = append(h.terminated, containerID)
		return nil
	}
	h.engine.PingFn = func(ctx context.Context) error { return nil }
	h.engine.AuditRunnersFn = func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
		return nil, nil
	}

	h.reconciler = orchestrator.NewReconciler(h.engine)
	rl := orchestrator.NewRemovalLogger(dataDir, "bootid42")
	h.ctrl = orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:              &mockPoolRepo{pools: []db.RunnerPool{}},
		ContainerEngine: h.engine,
		Reconciler:      h.reconciler,
		DataDir:         dataDir,
		RemovalLog:      rl,
	})
	return h
}

func (h *removalHarness) records(t *testing.T) []orchestrator.RemovalRecord {
	t.Helper()
	return readRemovalRecords(t, h.dataDir)
}

func TestTerminateAndRecord_ReapRecordsExitCodeAndCapture(t *testing.T) {
	h := newRemovalHarness(t)
	if err := h.ctrl.Boot(context.Background()); err != nil {
		t.Fatalf("boot: %v", err)
	}

	exit := 137
	h.engine.AuditRunnersFn = func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
		return []orchestrator.RunnerStatus{{
			ID: "dead-1", PoolID: 3, PoolName: "pool-a", State: "exited", ExitCode: exit,
		}}, nil
	}

	if err := h.ctrl.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	records := h.records(t)
	if len(records) != 1 {
		t.Fatalf("want exactly 1 removal record, got %d", len(records))
	}
	rec := records[0]
	if rec.Reason != orchestrator.RemovalReasonReap {
		t.Fatalf("reason = %q, want reap", rec.Reason)
	}
	if rec.ExitCode == nil || *rec.ExitCode != exit {
		t.Fatalf("exit code not recorded: %+v", rec)
	}
	if !rec.Capture.OK || rec.Capture.Bytes != int64(len("captured output")) {
		t.Fatalf("capture outcome not recorded: %+v", rec.Capture)
	}
	if rec.BootID != "bootid42" || rec.PoolID != 3 || rec.PoolName != "pool-a" {
		t.Fatalf("identity fields wrong: %+v", rec)
	}
	if len(h.terminated) != 1 || h.terminated[0] != "dead-1" {
		t.Fatalf("container not terminated: %v", h.terminated)
	}
}

func TestTerminateAndRecord_CaptureFailureNeverBlocksRemoval(t *testing.T) {
	h := newRemovalHarness(t)
	h.mu.Lock()
	h.captureErr = fmt.Errorf("%w: gone", orchestrator.ErrLogsUnavailable)
	h.mu.Unlock()

	if err := h.ctrl.TerminateRunner(context.Background(), 1, "ghost-9"); err != nil {
		t.Fatalf("manual termination must survive capture failure: %v", err)
	}

	records := h.records(t)
	if len(records) != 1 {
		t.Fatalf("want exactly 1 removal record, got %d", len(records))
	}
	rec := records[0]
	if rec.Reason != orchestrator.RemovalReasonManual {
		t.Fatalf("reason = %q, want manual", rec.Reason)
	}
	if rec.Capture.OK || rec.Capture.Error == "" {
		t.Fatalf("capture failure not recorded: %+v", rec.Capture)
	}
	if len(h.terminated) != 1 || h.terminated[0] != "ghost-9" {
		t.Fatalf("container not terminated despite capture failure: %v", h.terminated)
	}
}

func TestTerminateAndRecord_GracefulShutdownRecordsIdleRemovals(t *testing.T) {
	h := newRemovalHarness(t)
	if err := h.ctrl.Boot(context.Background()); err != nil {
		t.Fatalf("boot: %v", err)
	}

	idle := orchestrator.RunnerStatus{
		ID: "idle-1", Name: "runnero-p-a-1", PoolName: "pool-a", PoolID: 1,
		State: "running", SpawnedAt: time.Now().UTC(),
	}
	h.reconciler.TrackRunner(idle)

	if err := h.ctrl.GracefulShutdown(context.Background()); err != nil {
		t.Fatalf("graceful shutdown: %v", err)
	}

	records := h.records(t)
	if len(records) != 1 {
		t.Fatalf("want exactly 1 removal record, got %d", len(records))
	}
	rec := records[0]
	if rec.Reason != orchestrator.RemovalReasonShutdown {
		t.Fatalf("reason = %q, want shutdown", rec.Reason)
	}
	if rec.RunnerName != "runnero-p-a-1" {
		t.Fatalf("runner name lost: %+v", rec)
	}
	if !rec.Capture.OK {
		t.Fatalf("idle shutdown should still capture: %+v", rec.Capture)
	}
}

func TestTerminateAndRecord_DisabledPersistenceIsCleanNoop(t *testing.T) {
	h := newRemovalHarness(t)
	// Rebuild the controller without a RemovalLog (persistence off).
	ctrl := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:              &mockPoolRepo{pools: []db.RunnerPool{}},
		ContainerEngine: h.engine,
		Reconciler:      orchestrator.NewReconciler(h.engine),
		DataDir:         h.dataDir,
	})
	if err := ctrl.TerminateRunner(context.Background(), 0, "x-1"); err != nil {
		t.Fatalf("terminate without persistence: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.dataDir, "logs", "removals.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("removals.jsonl must not exist without persistence: %v", err)
	}
}
