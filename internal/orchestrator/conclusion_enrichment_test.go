package orchestrator_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/noosxe/runnero/internal/db"
	"github.com/noosxe/runnero/internal/orchestrator"
	"github.com/noosxe/runnero/internal/provider"
)

// jobsListerProvider wraps mockGitProvider with the optional
// provider.RunnerJobsLister capability (docs/21 §5.3), tracking every call so
// tests can assert the enrichment fast path fires exactly once per completion.
type jobsListerProvider struct {
	*mockGitProvider
	jobs       []provider.RunnerJob
	jobsErr    error
	jobsCalls  int
	gotScope   provider.RegistrationScope
	gotTarget  string
	gotForgeID int64
}

func (p *jobsListerProvider) RunnerLatestJobs(ctx context.Context, scope provider.RegistrationScope, targetURL string, runnerID int64) ([]provider.RunnerJob, error) {
	p.jobsCalls++
	p.gotScope = scope
	p.gotTarget = targetURL
	p.gotForgeID = runnerID
	if p.jobsErr != nil {
		return nil, p.jobsErr
	}
	return p.jobs, nil
}

// enrichHarness wires the busy-sync harness with a capability-carrying
// provider, a job-history recorder mock, and the enrichment toggle.
type enrichHarness struct {
	busySyncHarness
	prov *jobsListerProvider
	rec  *mockJobRecorder
}

func newEnrichHarness(t *testing.T, pool db.RunnerPool, enrich bool) *enrichHarness {
	t.Helper()
	h := &enrichHarness{
		busySyncHarness: busySyncHarness{
			mockProv:    &mockGitProvider{},
			liveRunners: make(map[string]orchestrator.RunnerStatus),
		},
	}
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
	}
	h.reconciler = orchestrator.NewReconciler(h.engine)
	h.ctrl = orchestrator.NewPoolController(orchestrator.ControllerOptions{
		DB:                &mockPoolRepo{pools: []db.RunnerPool{pool}},
		ContainerEngine:   h.engine,
		ProviderResolver:  resolver,
		Reconciler:        h.reconciler,
		JobRecorder:       h.rec,
		EnrichConclusions: enrich,
		Interval:          time.Hour,
	})
	if err := h.ctrl.Boot(context.Background()); err != nil {
		t.Fatalf("boot failed: %v", err)
	}
	return h
}

// TestConclusionEnrichment_EnrichesOnIdleTransition drives a full job cycle:
// idle → busy opens a transition row; busy → idle closes it with the forge's
// conclusion and external job id, queried once with the listing-carried forge
// id (docs/21 §5.3).
func TestConclusionEnrichment_EnrichesOnIdleTransition(t *testing.T) {
	ctx := context.Background()
	pool := busySyncPool("enrich", 0, 5)
	h := newEnrichHarness(t, pool, true)
	h.injectRunner(pool, "runnero-enrich-1", false)

	// Job picked up: the forge reports the runner busy, carrying its id.
	h.mockProv.remoteRunners = []provider.RemoteRunnerStatus{
		{ID: 7001, Name: "runnero-enrich-1", Busy: true, Online: true},
	}
	if err := h.ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile (busy) failed: %v", err)
	}
	if len(h.rec.opens) != 1 {
		t.Fatalf("expected 1 open row, got %d", len(h.rec.opens))
	}
	if h.prov.jobsCalls != 0 {
		t.Fatalf("enrichment must not fire on job start, got %d calls", h.prov.jobsCalls)
	}

	// Job finished: the forge reports idle; the conclusion lookup runs once.
	h.mockProv.remoteRunners = []provider.RemoteRunnerStatus{
		{ID: 7001, Name: "runnero-enrich-1", Busy: false, Online: true},
	}
	h.prov.jobs = []provider.RunnerJob{
		{ID: 9001, Conclusion: "success", CompletedAt: time.Now().UTC()},
	}
	if err := h.ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile (idle) failed: %v", err)
	}

	if h.prov.jobsCalls != 1 {
		t.Fatalf("expected exactly 1 enrichment call, got %d", h.prov.jobsCalls)
	}
	if h.prov.gotForgeID != 7001 || h.prov.gotScope != provider.ScopeRepo {
		t.Errorf("enrichment called with forgeID=%d scope=%v, want 7001 repo", h.prov.gotForgeID, h.prov.gotScope)
	}
	if h.prov.gotTarget != "https://github.com/my-org/my-repo" {
		t.Errorf("enrichment called with target %q", h.prov.gotTarget)
	}
	if len(h.rec.closes) != 1 {
		t.Fatalf("expected 1 close, got %d", len(h.rec.closes))
	}
	if h.rec.closes[0].status != "success" || h.rec.closes[0].jobID != 9001 {
		t.Errorf("close = (%s, %d), want (success, 9001)", h.rec.closes[0].status, h.rec.closes[0].jobID)
	}
}

// TestConclusionEnrichment_FailOpenOnError: an enrichment API failure must
// degrade to a plain completed row, never an error or a lost row (docs/21 G3).
func TestConclusionEnrichment_FailOpenOnError(t *testing.T) {
	ctx := context.Background()
	pool := busySyncPool("enrich-fail", 0, 5)
	h := newEnrichHarness(t, pool, true)
	h.injectRunner(pool, "runnero-enrich-2", false)

	h.mockProv.remoteRunners = []provider.RemoteRunnerStatus{
		{ID: 7002, Name: "runnero-enrich-2", Busy: true, Online: true},
	}
	if err := h.ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile (busy) failed: %v", err)
	}

	h.mockProv.remoteRunners = []provider.RemoteRunnerStatus{
		{ID: 7002, Name: "runnero-enrich-2", Busy: false, Online: true},
	}
	h.prov.jobsErr = errors.New("api down")
	if err := h.ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("enrichment failure must not fail the reconcile: %v", err)
	}
	if len(h.rec.closes) != 1 {
		t.Fatalf("expected 1 close, got %d", len(h.rec.closes))
	}
	if h.rec.closes[0].status != "completed" || h.rec.closes[0].jobID != 0 {
		t.Errorf("close = (%s, %d), want (completed, 0)", h.rec.closes[0].status, h.rec.closes[0].jobID)
	}
}

// TestConclusionEnrichment_DisabledByToggle: with the toggle off the forge is
// never asked and the row closes as plain completed (docs/21 §5.3).
func TestConclusionEnrichment_DisabledByToggle(t *testing.T) {
	ctx := context.Background()
	pool := busySyncPool("enrich-off", 0, 5)
	h := newEnrichHarness(t, pool, false)
	h.injectRunner(pool, "runnero-enrich-3", false)

	h.mockProv.remoteRunners = []provider.RemoteRunnerStatus{
		{ID: 7003, Name: "runnero-enrich-3", Busy: true, Online: true},
	}
	if err := h.ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile (busy) failed: %v", err)
	}
	h.mockProv.remoteRunners = []provider.RemoteRunnerStatus{
		{ID: 7003, Name: "runnero-enrich-3", Busy: false, Online: true},
	}
	if err := h.ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile (idle) failed: %v", err)
	}

	if h.prov.jobsCalls != 0 {
		t.Fatalf("disabled enrichment must not call the forge, got %d calls", h.prov.jobsCalls)
	}
	if len(h.rec.closes) != 1 || h.rec.closes[0].status != "completed" {
		t.Fatalf("expected one plain completed close, got %+v", h.rec.closes)
	}
}

// TestConclusionEnrichment_NoForgeIDSkipsCall: without a listing-carried id
// the lookup cannot be keyed, so it is skipped and the row degrades (docs/21 §5.3).
func TestConclusionEnrichment_NoForgeIDSkipsCall(t *testing.T) {
	ctx := context.Background()
	pool := busySyncPool("enrich-noid", 0, 5)
	h := newEnrichHarness(t, pool, true)
	h.injectRunner(pool, "runnero-enrich-4", false)

	h.mockProv.remoteRunners = []provider.RemoteRunnerStatus{
		{Name: "runnero-enrich-4", Busy: true, Online: true},
	}
	if err := h.ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile (busy) failed: %v", err)
	}
	h.mockProv.remoteRunners = []provider.RemoteRunnerStatus{
		{Name: "runnero-enrich-4", Busy: false, Online: true},
	}
	if err := h.ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile (idle) failed: %v", err)
	}

	if h.prov.jobsCalls != 0 {
		t.Fatalf("enrichment without forge id must not call the forge, got %d calls", h.prov.jobsCalls)
	}
	if len(h.rec.closes) != 1 || h.rec.closes[0].status != "completed" || h.rec.closes[0].jobID != 0 {
		t.Fatalf("expected plain completed close, got %+v", h.rec.closes)
	}
}

// TestConclusionEnrichment_UnconcludedJobSkipped: jobs the forge has not
// concluded (empty completed_at, e.g. busy-flag lag) are skipped rather than
// trusted; with nothing concluded the row degrades to completed (docs/21 §5.3).
func TestConclusionEnrichment_UnconcludedJobSkipped(t *testing.T) {
	ctx := context.Background()
	pool := busySyncPool("enrich-running", 0, 5)
	h := newEnrichHarness(t, pool, true)
	h.injectRunner(pool, "runnero-enrich-5", false)

	h.mockProv.remoteRunners = []provider.RemoteRunnerStatus{
		{ID: 7005, Name: "runnero-enrich-5", Busy: true, Online: true},
	}
	if err := h.ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile (busy) failed: %v", err)
	}
	h.mockProv.remoteRunners = []provider.RemoteRunnerStatus{
		{ID: 7005, Name: "runnero-enrich-5", Busy: false, Online: true},
	}
	h.prov.jobs = []provider.RunnerJob{
		{ID: 9002, Conclusion: ""},
	}
	if err := h.ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile (idle) failed: %v", err)
	}

	if h.prov.jobsCalls != 1 {
		t.Fatalf("expected the enrichment call to fire, got %d", h.prov.jobsCalls)
	}
	if len(h.rec.closes) != 1 || h.rec.closes[0].status != "completed" || h.rec.closes[0].jobID != 0 {
		t.Fatalf("expected degraded completed close, got %+v", h.rec.closes)
	}
}

// TestConclusionEnrichment_EnrichesOnCleanDeath: poll-fallback pools receive
// no webhooks, so the death path itself must enrich a clean (code 0) exit —
// the busy→idle race is otherwise always lost to the container exiting first
// (docs/21 §5.3). Unclean exits stay interrupted with no enrichment call.
func TestConclusionEnrichment_EnrichesOnCleanDeath(t *testing.T) {
	ctx := context.Background()
	pool := busySyncPool("enrich-death", 0, 5)
	h := newEnrichHarness(t, pool, true)
	h.injectRunner(pool, "runnero-death-1", false)

	// Job picked up: busy-sync opens the transition row and records the id.
	h.mockProv.remoteRunners = []provider.RemoteRunnerStatus{
		{ID: 7006, Name: "runnero-death-1", Busy: true, Online: true},
	}
	if err := h.ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile (busy) failed: %v", err)
	}
	if len(h.rec.opens) != 1 {
		t.Fatalf("expected 1 open row, got %d", len(h.rec.opens))
	}

	// The forge still reports busy (flag lag), but the container is already
	// gone: exit code 0. The audit cycle reaps it and the death path enriches.
	h.liveMu.Lock()
	dead := h.liveRunners["container-runnero-death-1"]
	dead.State = "exited"
	dead.ExitCode = 0
	h.liveRunners[dead.ID] = dead
	h.liveMu.Unlock()
	h.prov.jobs = []provider.RunnerJob{
		{ID: 9004, Conclusion: "success", CompletedAt: time.Now().UTC()},
	}
	if err := h.ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile (death) failed: %v", err)
	}

	if h.prov.jobsCalls != 1 {
		t.Fatalf("expected exactly 1 death-path enrichment call, got %d", h.prov.jobsCalls)
	}
	if len(h.rec.closes) != 1 {
		t.Fatalf("expected 1 close, got %d", len(h.rec.closes))
	}
	if h.rec.closes[0].status != "success" || h.rec.closes[0].jobID != 9004 {
		t.Errorf("close = (%s, %d), want (success, 9004)", h.rec.closes[0].status, h.rec.closes[0].jobID)
	}
}

// TestConclusionEnrichment_UncleanDeathStaysInterrupted: a non-zero exit must
// close as interrupted without consulting the forge — the job outcome is
// unknowable when the runner died mid-job (docs/21 §5.2).
func TestConclusionEnrichment_UncleanDeathStaysInterrupted(t *testing.T) {
	ctx := context.Background()
	pool := busySyncPool("enrich-crash", 0, 5)
	h := newEnrichHarness(t, pool, true)
	h.injectRunner(pool, "runnero-crash-1", false)

	h.mockProv.remoteRunners = []provider.RemoteRunnerStatus{
		{ID: 7007, Name: "runnero-crash-1", Busy: true, Online: true},
	}
	if err := h.ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile (busy) failed: %v", err)
	}

	h.liveMu.Lock()
	dead := h.liveRunners["container-runnero-crash-1"]
	dead.State = "exited"
	dead.ExitCode = 137
	h.liveRunners[dead.ID] = dead
	h.liveMu.Unlock()
	if err := h.ctrl.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile (crash) failed: %v", err)
	}

	if h.prov.jobsCalls != 0 {
		t.Fatalf("unclean exit must not consult the forge, got %d calls", h.prov.jobsCalls)
	}
	if len(h.rec.closes) != 1 || h.rec.closes[0].status != "interrupted" || h.rec.closes[0].jobID != 0 {
		t.Fatalf("expected interrupted close, got %+v", h.rec.closes)
	}
}
