package server_test

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/noosxe/runnero/internal/db"
	"github.com/noosxe/runnero/internal/orchestrator"
	supervisorv1 "github.com/noosxe/runnero/internal/pb/supervisor/v1"
)

// TestPoolServiceUpdatePoolDoesNotBlockOnRecycle drives UpdatePool against a
// real PoolController whose container engine blocks on removal: the
// spawn-identity edit must answer while the recycle is still in flight, the
// recycle must then complete in the background, a failing removal must not
// fail the RPC, and shutdown mid-recycle must not hang (docs/22 §5.5).
func TestPoolServiceUpdatePoolDoesNotBlockOnRecycle(t *testing.T) {
	ctx := context.Background()
	database := setupTestDB(t)

	engine := &orchestrator.MockContainerProvider{}
	reconciler := orchestrator.NewReconciler(engine)
	ctrl := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:              database,
		ContainerEngine: engine,
		Reconciler:      reconciler,
	})
	_, client, rawCookie := startPoolEditTestServer(t, database, ctrl)

	profile, err := database.CreateAuthProfile(ctx, db.CreateAuthProfileParams{
		Name:           "recycle-profile",
		AuthMethod:     "pat",
		TokenEncrypted: sql.NullString{String: "encrypted-token", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateAuthProfile failed: %v", err)
	}
	createReq := connect.NewRequest(&supervisorv1.CreatePoolRequest{
		Pool: editPoolPayload(0, profile.ID, "recycle-pool"),
	})
	createReq.Header().Set("Cookie", "session_token="+rawCookie)
	createRes, err := client.CreatePool(ctx, createReq)
	if err != nil {
		t.Fatalf("CreatePool failed: %v", err)
	}
	poolID := createRes.Msg.Pool.Id

	// Two idle runners of the pool, adopted from the engine.
	engine.AuditRunnersFn = func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
		return []orchestrator.RunnerStatus{
			{PoolID: poolID, ID: "idle-1", Name: "runnero-recycle-1", PoolName: "recycle-pool", State: "running"},
			{PoolID: poolID, ID: "idle-2", Name: "runnero-recycle-2", PoolName: "recycle-pool", State: "running"},
		}, nil
	}
	if _, err := reconciler.RebuildState(ctx); err != nil {
		t.Fatalf("RebuildState failed: %v", err)
	}

	release := make(chan struct{})
	started := make(chan struct{}, 4)
	var removed, failed atomic.Int32
	engine.TerminateRunnerFn = func(ctx context.Context, id string) error {
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		if id == "idle-2" {
			failed.Add(1)
			return errors.New("docker: removal failed")
		}
		removed.Add(1)
		return nil
	}

	// Spawn-identity edit: answered while the first removal is blocked.
	identity := editPoolPayload(poolID, profile.ID, "recycle-pool")
	identity.Labels = []string{"self-hosted", "gpu"}
	type result struct {
		pool *supervisorv1.Pool
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		p, err := updatePool(t, client, rawCookie, identity)
		resCh <- result{p, err}
	}()
	select {
	case res := <-resCh:
		if res.err != nil {
			t.Fatalf("spawn-identity update failed: %v", res.err)
		}
		if got := res.pool.Labels; len(got) != 2 || got[1] != "gpu" {
			t.Errorf("response must carry the persisted pool, labels=%v", got)
		}
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("UpdatePool blocked on the idle recycle")
	}

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("background recycle never reached the container engine")
	}
	if removed.Load() != 0 || failed.Load() != 0 {
		t.Fatal("recycle must still be in flight when the RPC answered")
	}

	// The recycle completes in the background; the failing removal is
	// logged by the controller and never surfaced to the (finished) RPC.
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for removed.Load()+failed.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("background recycle did not finish: removed=%d failed=%d", removed.Load(), failed.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Another edit, then shutdown mid-recycle: shutdown cancels the pass.
	var calls atomic.Int32
	inFlight := make(chan struct{})
	engine.TerminateRunnerFn = func(ctx context.Context, id string) error {
		if calls.Add(1) > 1 {
			return nil // shutdown's own teardown returns at once
		}
		close(inFlight)
		<-ctx.Done()
		return ctx.Err()
	}
	if _, err := reconciler.RebuildState(ctx); err != nil {
		t.Fatalf("RebuildState failed: %v", err)
	}
	identity.Labels = []string{"self-hosted", "arm64"}
	if _, err := updatePool(t, client, rawCookie, identity); err != nil {
		t.Fatalf("second spawn-identity update failed: %v", err)
	}
	select {
	case <-inFlight:
	case <-time.After(5 * time.Second):
		t.Fatal("second recycle never reached the container engine")
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	start := time.Now()
	_ = ctrl.GracefulShutdown(shutdownCtx)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("shutdown mid-recycle took %v", elapsed)
	}
}
