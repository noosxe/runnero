package orchestrator_test

// Provider-vetoed idle drains (RUN-182): when the provider refuses to
// deregister a runner because it is mid-job (GitHub 422 "currently running
// a job", surfaced as provider.ErrRunnerBusy), scale-down must preserve the
// runner and re-mark it busy locally instead of terminating it — a stale
// local idle classification (e.g. right after a supervisor restart) must
// never kill a runner the forge still considers busy.

import (
	"context"
	"fmt"
	"testing"

	"github.com/noosxe/runnero/internal/db"
	"github.com/noosxe/runnero/internal/orchestrator"
	"github.com/noosxe/runnero/internal/provider"
)

// busyDeregErr mimics the GitHub provider's wrapped 422 response so the
// errors.Is unwrapping path is exercised end-to-end.
func busyDeregErr(runnerName string) error {
	return fmt.Errorf("failed to delete registered runner %q (status 422): {\"message\":\"Bad request - Runner %q is currently running a job and cannot be deleted.\"}: %w",
		runnerName, runnerName, provider.ErrRunnerBusy)
}

func newVetoHarness(t *testing.T, minIdle int64, fleet []orchestrator.RunnerStatus) (*orchestrator.PoolController, *orchestrator.MockContainerProvider, *orchestrator.Reconciler, *mockGitProvider) {
	t.Helper()
	pool := db.RunnerPool{
		ID:             318,
		Name:           "veto-pool",
		Provider:       "github",
		RepositoryUrl:  "https://github.com/owner/repo",
		Scope:          "repo",
		AuthProfileID:  30,
		MinIdleRunners: minIdle,
		MaxConcurrency: 5,
		RunnerImage:    "ghcr.io/noosxe/runnero:latest",
	}
	repo := &mockPoolRepo{pools: []db.RunnerPool{pool}}
	gitProv := &mockGitProvider{}
	resolver := &mockGitProviderResolver{
		providers: map[int64]provider.GitProvider{30: gitProv},
	}
	mockEngine := &orchestrator.MockContainerProvider{
		AuditRunnersFn: func(ctx context.Context) ([]orchestrator.RunnerStatus, error) {
			return fleet, nil
		},
	}
	reconciler := orchestrator.NewReconciler(mockEngine)
	ctrl := orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:               repo,
		ContainerEngine:  mockEngine,
		ProviderResolver: resolver,
		Reconciler:       reconciler,
	})
	return ctrl, mockEngine, reconciler, gitProv
}

func vetoFleet() []orchestrator.RunnerStatus {
	return []orchestrator.RunnerStatus{
		{PoolID: 318, ID: "r-a", Name: "runnero-veto-pool-a", PoolName: "veto-pool", State: "running"},
		{PoolID: 318, ID: "r-b", Name: "runnero-veto-pool-b", PoolName: "veto-pool", State: "running"},
		{PoolID: 318, ID: "r-c", Name: "runnero-veto-pool-c", PoolName: "veto-pool", State: "running"},
	}
}

// TestExcessIdleDrain_VetoPreservesBusyRunner verifies the fixed-target drain
// (RUN-42 path) respects the provider veto: the mid-job runner survives and
// is re-marked busy, while the remaining excess is still drained from other
// candidates (docs/03 §3.5).
func TestExcessIdleDrain_VetoPreservesBusyRunner(t *testing.T) {
	ctrl, engine, rec, gitProv := newVetoHarness(t, 1, vetoFleet())
	ctx := context.Background()
	gitProv.deregErrByName = map[string]error{
		"runnero-veto-pool-a": busyDeregErr("runnero-veto-pool-a"),
	}

	if err := ctrl.Boot(ctx); err != nil {
		t.Fatalf("ctrl.Boot failed: %v", err)
	}

	terminated := terminatedIDSet(engine)
	if terminated["r-a"] {
		t.Fatal("provider-vetoed runner must not be terminated")
	}
	if !terminated["r-b"] || !terminated["r-c"] {
		t.Fatalf("non-vetoed excess runners must still be drained, terminated=%v", terminated)
	}

	tracked := rec.TrackedPoolRunners(318)
	if len(tracked) != 1 || tracked[0].ID != "r-a" {
		t.Fatalf("only the vetoed runner must stay tracked, got %+v", tracked)
	}
	if !tracked[0].IsBusy {
		t.Error("vetoed runner must be re-marked busy so classification converges")
	}
	for _, name := range []string{"runnero-veto-pool-b", "runnero-veto-pool-c"} {
		if !containsString(gitProv.deregistered, name) {
			t.Errorf("runner %s must have been deregistered, got %v", name, gitProv.deregistered)
		}
	}
	if containsString(gitProv.deregistered, "runnero-veto-pool-a") {
		t.Errorf("vetoed runner deregistration must not report success, got %v", gitProv.deregistered)
	}
}

// TestScaleToZeroDrain_VetoPreservesBusyRunner verifies the scale-to-zero
// drain (RUN-71 era path) honors the veto for stale standbys (docs/03 §3.5).
func TestScaleToZeroDrain_VetoPreservesBusyRunner(t *testing.T) {
	ctrl, engine, rec, gitProv := newVetoHarness(t, 0, vetoFleet())
	ctx := context.Background()
	gitProv.deregErrByName = map[string]error{
		"runnero-veto-pool-a": busyDeregErr("runnero-veto-pool-a"),
		"runnero-veto-pool-b": busyDeregErr("runnero-veto-pool-b"),
		"runnero-veto-pool-c": busyDeregErr("runnero-veto-pool-c"),
	}

	if err := ctrl.Boot(ctx); err != nil {
		t.Fatalf("ctrl.Boot failed: %v", err)
	}

	if len(engine.TerminatedIDs) != 0 {
		t.Fatalf("every drain was vetoed, nothing may be terminated, got %v", engine.TerminatedIDs)
	}
	for _, r := range rec.TrackedPoolRunners(318) {
		if !r.IsBusy {
			t.Errorf("vetoed runner %s must be re-marked busy", r.ID)
		}
	}
}

// TestExcessIdleDrain_GenericDeregisterErrorKeepsLegacyTermination locks the
// boundary of the veto: only ErrRunnerBusy preserves a runner; any other
// deregistration failure keeps the legacy best-effort termination (the ghost
// sweep reconciles the orphaned registration afterwards, docs/20 §4).
func TestExcessIdleDrain_GenericDeregisterErrorKeepsLegacyTermination(t *testing.T) {
	ctx := context.Background()

	ctrl, engine, _, gitProv := newVetoHarness(t, 1, vetoFleet())
	gitProv.deregErrByName = map[string]error{
		"runnero-veto-pool-a": fmt.Errorf("failed to delete registered runner %q (status 403): forbidden", "runnero-veto-pool-a"),
	}

	gitProv.deregErrByName = map[string]error{
		"runnero-veto-pool-a": fmt.Errorf("failed to delete registered runner %q (status 403): forbidden", "runnero-veto-pool-a"),
	}

	if err := ctrl.Boot(ctx); err != nil {
		t.Fatalf("ctrl.Boot failed: %v", err)
	}

	terminated := terminatedIDSet(engine)
	if !terminated["r-a"] || !terminated["r-b"] {
		t.Fatalf("generic deregister errors must not veto the drain (legacy behavior), terminated=%v", terminated)
	}
	if terminated["r-c"] {
		t.Fatal("excess is 2: the third runner must be spared once excess is satisfied")
	}
}

// TestRecycleIdleRunners_VetoPreservesBusyRunner verifies the recycle path
// (docs/22 §6.2) also respects the provider veto: a runner whose local idle
// state is stale is preserved and re-marked busy instead of being recycled.
func TestRecycleIdleRunners_VetoPreservesBusyRunner(t *testing.T) {
	ctrl, engine, rec, gitProv := newVetoHarness(t, 3, vetoFleet())
	ctx := context.Background()

	if err := ctrl.Boot(ctx); err != nil {
		t.Fatalf("ctrl.Boot failed: %v", err)
	}
	if len(engine.TerminatedIDs) != 0 {
		t.Fatalf("boot with idle==target must not drain, got %v", engine.TerminatedIDs)
	}

	gitProv.deregErrByName = map[string]error{
		"runnero-veto-pool-a": busyDeregErr("runnero-veto-pool-a"),
	}
	if err := ctrl.RecycleIdleRunners(ctx, 318); err != nil {
		t.Fatalf("RecycleIdleRunners failed: %v", err)
	}

	terminated := terminatedIDSet(engine)
	if terminated["r-a"] {
		t.Fatal("provider-vetoed runner must not be recycled")
	}
	if !terminated["r-b"] || !terminated["r-c"] {
		t.Fatalf("non-vetoed idle runners must still be recycled, terminated=%v", terminated)
	}
	tracked := rec.TrackedPoolRunners(318)
	if len(tracked) != 1 || tracked[0].ID != "r-a" || !tracked[0].IsBusy {
		t.Fatalf("vetoed runner must stay tracked and be re-marked busy, got %+v", tracked)
	}
}

func containsString(list []string, want string) bool {
	for _, got := range list {
		if got == want {
			return true
		}
	}
	return false
}
