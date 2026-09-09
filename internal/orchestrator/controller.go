package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/noosxe/runnero/internal/db"
	"github.com/noosxe/runnero/internal/logging"
	"github.com/noosxe/runnero/internal/provider"
	"github.com/noosxe/runnero/internal/server"
)

var (
	// ErrControllerStopped is returned when an operation cannot be performed on a stopped controller.
	ErrControllerStopped = errors.New("controller is stopped")
	// ErrEngineUnreachable is returned when the container engine is unreachable during boot.
	ErrEngineUnreachable = errors.New("container engine is unreachable")
)

const (
	// DefaultControlLoopInterval is the default period between reconciliation cycles (10s per docs/03 §3).
	DefaultControlLoopInterval = 10 * time.Second

	// DefaultHeartbeatTimeout is the maximum duration between heartbeats before the auditor is marked degraded.
	DefaultHeartbeatTimeout = 30 * time.Second

	// DefaultTotalAllowedRunners is the default global circuit breaker limit across all pools (OQ #4, docs/05 §3).
	DefaultTotalAllowedRunners = 20
	// DefaultShutdownTimeout is the default duration to wait for active runners to complete during SIGTERM (OQ #24).
	DefaultShutdownTimeout = 300 * time.Second

	// DefaultShutdownPollInterval is the frequency to poll active containers during graceful shutdown (docs/03 §7).
	DefaultShutdownPollInterval = 5 * time.Second

	// DefaultScaleToZeroGracePeriod is the idle startup/job-pickup window before an on-demand runner is considered orphaned (RUN-71).
	DefaultScaleToZeroGracePeriod = 5 * time.Minute

	// DefaultGhostSweepOfflineCycles is how many consecutive audit cycles a listing entry must
	// be offline and locally-untracked before the ghost sweep deregisters it (docs/20 §4.1).
	DefaultGhostSweepOfflineCycles = 3

	// DefaultGhostSweepMaxDeregistrations bounds the per-cycle sweep burst after mass-leak
	// events (e.g. host reboot with a large pool); the remainder is swept on later cycles.
	DefaultGhostSweepMaxDeregistrations = 50
	// DefaultDrainBackstop is the force-terminate backstop for gracefully
	// drained busy runners whose deleted pool set no max_runner_lifetime
	// (docs/25 §4.4, §8.3): drained leftovers can never outlive this cap.
	DefaultDrainBackstop = 6 * time.Hour
)

// ProvisionRequest represents a queued runner provisioning request when the global quota is saturated.
type ProvisionRequest struct {
	PoolID    int64     `json:"pool_id"`
	PoolName  string    `json:"pool_name,omitempty"` // logging aid; spawn identity is the DB row
	TargetURL string    `json:"target_url,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// JobHistoryRecorder records runner job execution statuses into the database (docs/03 §4, §7).
// The lifecycle methods (docs/21 §5.2) maintain one open job_history row per busy runner:
// opened when the busy-state sync observes idle→busy, closed on busy→idle, runner death,
// timeout, or crash recovery. All implementations must fail open from the caller's perspective.
type JobHistoryRecorder interface {
	RecordJobTimeout(ctx context.Context, poolID int64, runnerName, logPath string, startedAt, completedAt time.Time) error
	OpenTransitionJob(ctx context.Context, poolID int64, runnerName string, startedAt time.Time) error
	CloseTransitionJob(ctx context.Context, poolID int64, runnerName, status string, jobID int64, logPath string, completedAt time.Time) error
	CloseInterruptedOpenJobs(ctx context.Context, completedAt time.Time) (int64, error)
	CloseStaleOpenJobs(ctx context.Context, poolID int64, cutoff, completedAt time.Time) (int64, error)

	// Webhook enrichment (docs/21 §5.5): upsert/merge/close rows keyed by the
	// forge's external job id. Best-effort like the transition methods —
	// failures must never fail the webhook handling itself.
	RecordWebhookQueued(ctx context.Context, poolID, jobID int64, meta db.WebhookJobMeta, queuedAt time.Time) error
	RecordWebhookStarted(ctx context.Context, poolID, jobID int64, runnerName string, startedAt, queuedAt time.Time, meta db.WebhookJobMeta) error
	RecordWebhookCompleted(ctx context.Context, poolID, jobID int64, runnerName, status string, completedAt time.Time) error
}

// AppSettingsReader reads application-wide configuration from the database (docs/02 §4).
type AppSettingsReader interface {
	GetAppSetting(ctx context.Context, key string) (db.AppSetting, error)
}

// PoolRepository abstracts loading active runner pools from the database.
type PoolRepository interface {
	ListRunnerPools(ctx context.Context) ([]db.RunnerPool, error)
}

// PoolTargetsRepository abstracts loading pool targets from the database.
type PoolTargetsRepository interface {
	ListPoolTargetsByPoolId(ctx context.Context, poolID int64) ([]db.PoolTarget, error)
}

// TaskExitHandler processes termination events for one-off task containers (e.g. Renovate bot) (docs/03 §5).
type TaskExitHandler interface {
	HandleContainerExit(ctx context.Context, containerID string, exitCode int, logPath string) (bool, error)
}

// GitProviderResolver resolves a GitProvider instance for a given auth profile ID.
type GitProviderResolver interface {
	ResolveProvider(ctx context.Context, authProfileID int64) (provider.GitProvider, error)
}

// RegistryAdapter adapts a *provider.Registry and *db.DB into a GitProviderResolver.
type RegistryAdapter struct {
	Database *db.DB
	Registry *provider.Registry
}

// ResolveProvider loads the decrypted auth profile and builds the GitProvider.
func (a *RegistryAdapter) ResolveProvider(ctx context.Context, authProfileID int64) (provider.GitProvider, error) {
	if a.Database == nil || a.Registry == nil {
		return nil, fmt.Errorf("database or provider registry is nil")
	}
	return a.Registry.BuildFromDB(ctx, a.Database, authProfileID)
}

// ControllerState represents the operational status of the lifecycle controller.
type ControllerState string

const (
	StateStopped ControllerState = "stopped"
	StateBooting ControllerState = "booting"
	StateRunning ControllerState = "running"
	StatePaused  ControllerState = "paused"
)

// ControllerOptions configures the PoolController.
type ControllerOptions struct {
	DB                           PoolRepository
	JobRecorder                  JobHistoryRecorder
	ContainerEngine              ContainerProvider
	ProviderResolver             GitProviderResolver
	Reconciler                   *Reconciler
	EventListener                *EventListener
	DataDir                      string
	GlobalMaxRunners             int
	ShutdownTimeout              time.Duration
	ShutdownPollInterval         time.Duration
	Interval                     time.Duration
	ScaleToZeroGracePeriod       time.Duration
	TaskExitHandler              TaskExitHandler
	GhostSweepOfflineCycles      int
	GhostSweepMaxDeregistrations int
	// EnrichConclusions enables conclusion enrichment on job completion
	// (docs/21 §5.3): when a busy-to-idle transition closes a job row, the
	// controller asks the forge for the runner's latest job once and stores
	// the external job id and conclusion. Effective only when the resolved
	// provider implements RunnerJobsLister; failures fail open. Enabled by
	// the enrich-job-conclusions config default.
	EnrichConclusions bool
}

// PoolController orchestrates the lifecycle control loop across all runner pools (docs/03 §1).
type PoolController struct {
	mu                      sync.RWMutex
	provisionMu             sync.Mutex // single-writer provisioning lock (RUN-38)
	db                      PoolRepository
	jobRecorder             JobHistoryRecorder
	engine                  ContainerProvider
	providerResolver        GitProviderResolver
	reconciler              *Reconciler
	eventListener           *EventListener
	taskExitHandler         TaskExitHandler
	dataDir                 string
	globalMaxRunners        int
	shutdownTimeout         time.Duration
	shutdownPollInterval    time.Duration
	interval                time.Duration
	scaleToZeroGracePeriod  time.Duration
	ghostOfflineCycles      int
	ghostMaxDeregistrations int
	enrichConclusions       bool
	drainMu                 sync.Mutex
	drainingPools           map[int64]drainEntry // pool id -> drain metadata for gracefully deleted pools (docs/25 §4.2)
	ghostMu                 sync.Mutex
	ghostCounters           map[int64]map[string]int // pool id -> runner name -> consecutive offline-untracked cycles (docs/20 §4.1)

	pollMu      sync.Mutex
	lastPollAt  map[int64]time.Time // pool id -> last completed demand poll (docs/24 §5.4)
	pollBackoff map[int64]int       // pool id -> consecutive poll failures; each adds one interval (cap 5×)
	jitterFn    func() float64      // returns [0,1); injectable for deterministic tests

	poolIDsMu     sync.RWMutex
	poolIDsByName map[string]int64 // spawn-time name -> pool id; resolves legacy events/containers (RUN-126)

	queue         []ProvisionRequest // internal provisioning queue for quota saturation (RUN-39)
	state         ControllerState
	lastHeartbeat time.Time
	logger        *slog.Logger

	diagMu      sync.RWMutex
	diagnostics map[int64]PoolDiagnosticState
}

// NewPoolController creates a new lifecycle control loop engine.
func NewPoolController(opts ControllerOptions) *PoolController {
	if opts.Interval <= 0 {
		opts.Interval = DefaultControlLoopInterval
	}
	if opts.Reconciler == nil && opts.ContainerEngine != nil {
		opts.Reconciler = NewReconciler(opts.ContainerEngine)
	}

	globalMax := opts.GlobalMaxRunners
	if globalMax <= 0 {
		globalMax = DefaultTotalAllowedRunners
	}

	shutdownTimeout := opts.ShutdownTimeout
	if shutdownTimeout <= 0 {
		shutdownTimeout = DefaultShutdownTimeout
	}
	shutdownPollInterval := opts.ShutdownPollInterval
	if shutdownPollInterval <= 0 {
		shutdownPollInterval = DefaultShutdownPollInterval
	}

	jobRec := opts.JobRecorder
	if jobRec == nil && opts.DB != nil {
		if rec, ok := opts.DB.(JobHistoryRecorder); ok {
			jobRec = rec
		}
	}

	gracePeriod := opts.ScaleToZeroGracePeriod
	if gracePeriod <= 0 {
		gracePeriod = DefaultScaleToZeroGracePeriod
	}

	ghostCycles := opts.GhostSweepOfflineCycles
	if ghostCycles <= 0 {
		ghostCycles = DefaultGhostSweepOfflineCycles
	}
	ghostMax := opts.GhostSweepMaxDeregistrations
	if ghostMax <= 0 {
		ghostMax = DefaultGhostSweepMaxDeregistrations
	}

	ctrl := PoolController{
		db:                      opts.DB,
		jobRecorder:             jobRec,
		engine:                  opts.ContainerEngine,
		providerResolver:        opts.ProviderResolver,
		reconciler:              opts.Reconciler,
		eventListener:           opts.EventListener,
		taskExitHandler:         opts.TaskExitHandler,
		dataDir:                 opts.DataDir,
		globalMaxRunners:        globalMax,
		shutdownTimeout:         shutdownTimeout,
		shutdownPollInterval:    shutdownPollInterval,
		interval:                opts.Interval,
		scaleToZeroGracePeriod:  gracePeriod,
		state:                   StateStopped,
		ghostOfflineCycles:      ghostCycles,
		ghostMaxDeregistrations: ghostMax,
		enrichConclusions:       opts.EnrichConclusions,
		ghostCounters:           make(map[int64]map[string]int),
		lastPollAt:              make(map[int64]time.Time),
		pollBackoff:             make(map[int64]int),
		jitterFn:                func() float64 { return rand.Float64() },
		poolIDsByName:           make(map[string]int64),
		logger:                  logging.For("controller"),
		drainingPools:           make(map[int64]drainEntry),
		diagnostics:             make(map[int64]PoolDiagnosticState),
	}

	// Legacy adoption (RUN-126): containers spawned before the pool-id label
	// existed carry only a pool-name label; resolve those by name at audit time
	// so boot-time adoption of in-flight runners survives the upgrade (docs/03 §2).
	if ctrl.reconciler != nil && ctrl.db != nil {
		ctrl.reconciler.SetPoolNameResolver(ctrl.resolvePoolIDByName)
	}

	return &ctrl
}

// resolvePoolIDByName maps a pool name to its database id, falling back to a
// direct database lookup on cache misses (boot-time audits run before the
// first Reconcile refreshes the cache).
func (c *PoolController) resolvePoolIDByName(name string) (int64, bool) {
	c.poolIDsMu.RLock()
	id, ok := c.poolIDsByName[name]
	c.poolIDsMu.RUnlock()
	if ok {
		return id, true
	}

	if c.db == nil {
		return 0, false
	}
	pools, err := c.db.ListRunnerPools(context.Background())
	if err != nil {
		return 0, false
	}
	c.refreshPoolIDs(pools)
	c.poolIDsMu.RLock()
	defer c.poolIDsMu.RUnlock()
	id, ok = c.poolIDsByName[name]
	return id, ok
}

// refreshPoolIDs rebuilds the name->id cache from the current pool list.
func (c *PoolController) refreshPoolIDs(pools []db.RunnerPool) {
	c.poolIDsMu.Lock()
	defer c.poolIDsMu.Unlock()
	c.poolIDsByName = make(map[string]int64, len(pools))
	for _, p := range pools {
		c.poolIDsByName[p.Name] = p.ID
	}
}

// State returns the current lifecycle state of the controller.
func (c *PoolController) State() ControllerState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state
}

// Pause temporarily halts runner provisioning and pool reconciliation.
func (c *PoolController) Pause() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == StateRunning {
		c.state = StatePaused
		c.logger.Info("pool controller paused")
	}
}

// Resume unpauses the controller, restoring active reconciliation.
func (c *PoolController) Resume() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == StatePaused {
		c.state = StateRunning
		c.logger.Info("pool controller resumed")
	}
}

// Boot executes the structured startup sequence per docs/03 §1:
// 1. Rebuild in-memory container state from host engine
// 2. Verify container engine connectivity
// 3. Load active runner pools from DB
// 4. Validate provider credentials
// 5. Provision min_idle_runners per pool
func (c *PoolController) Boot(ctx context.Context) error {
	c.mu.Lock()
	c.state = StateBooting
	c.mu.Unlock()

	c.logger.Info("executing control loop boot sequence")

	// 1. Rebuild state by querying supervisor-managed containers
	if c.reconciler != nil {
		report, err := c.reconciler.RebuildState(ctx)
		if err != nil {
			c.logger.Warn("failed to rebuild state from engine during boot", "err", err)
		} else {
			c.logger.Info("boot state sync completed", "adopted", len(report.Adopted), "active", len(report.Active))
		}
	}

	// Crash recovery (docs/21 §5.4): any job_history row still open from a
	// previous supervisor lifetime is closed as 'interrupted' before the
	// initial convergence pass re-opens rows for currently-busy runners.
	if c.jobRecorder != nil {
		if n, err := c.jobRecorder.CloseInterruptedOpenJobs(ctx, time.Now().UTC()); err != nil {
			c.logger.Warn("boot job-row recovery failed", "err", err)
		} else if n > 0 {
			c.logger.Info("closed interrupted job rows from previous run", "count", n)
		}
	}

	// 2. Verify container engine connectivity
	if c.engine != nil {
		if err := c.engine.Ping(ctx); err != nil {
			c.mu.Lock()
			c.state = StateStopped
			c.mu.Unlock()
			return fmt.Errorf("%w: %v", ErrEngineUnreachable, err)
		}
	}

	// 3. Load active pools
	pools, err := c.loadPools(ctx)
	if err != nil {
		c.mu.Lock()
		c.state = StateStopped
		c.mu.Unlock()
		return fmt.Errorf("loading pools during boot: %w", err)
	}

	// 4. Validate provider credentials & 5. Provision min_idle_runners
	for _, p := range pools {
		if err := c.validateAndProvisionPool(ctx, p); err != nil {
			c.logger.Error("failed validating or provisioning pool on boot", "pool", p.Name, "err", err)
		}
	}

	c.mu.Lock()
	c.state = StateRunning
	c.lastHeartbeat = time.Now()
	c.mu.Unlock()

	c.logger.Info("control loop boot completed successfully", "pools", len(pools))
	return nil
}

// Reconcile executes a single reconciliation cycle across all pools:
// audits running containers, detects exited containers, reaps them, and provisions replacements (docs/03 §1, §4).
func (c *PoolController) Reconcile(ctx context.Context) error {
	c.provisionMu.Lock()
	defer c.provisionMu.Unlock()

	c.mu.RLock()
	state := c.state
	c.mu.RUnlock()

	if state == StatePaused || state == StateStopped {
		return nil
	}

	// 1. Audit containers across all pools
	if c.reconciler != nil {
		report, err := c.reconciler.Audit(ctx)
		if err != nil {
			c.logger.Warn("audit cycle failed", "err", err)
		} else {
			// Reap any exited containers detected during audit cycle
			for _, exited := range report.Exited {
				c.reapContainer(ctx, exited.ID, exited.PoolID, exited.ExitCode, true)
			}
		}
	}

	// 2. Refresh runtime settings from DB if available (docs/02 §4)
	if reader, ok := c.db.(AppSettingsReader); ok {
		if setting, err := reader.GetAppSetting(ctx, "total_allowed_runners"); err == nil {
			if val, err := strconv.Atoi(setting.Value); err == nil && val > 0 {
				c.globalMaxRunners = val
			}
		}
	}

	// 3. Drain queued provisioning requests if global capacity has freed
	c.drainQueue(ctx)

	// 4. Load active pools from DB
	pools, err := c.loadPools(ctx)
	if err != nil {
		return fmt.Errorf("loading pools for reconcile: %w", err)
	}
	c.refreshPoolIDs(pools)

	// 5. Detect removed pools and drain their runners (keyed by pool id so
	// renames never look like deletes; RUN-126, docs/22 §5.4)
	currentPools := make(map[int64]struct{}, len(pools))
	for _, p := range pools {
		currentPools[p.ID] = struct{}{}
	}

	type removedPool struct {
		id   int64
		name string
	}
	var removedPools []removedPool
	if c.reconciler != nil {
		c.reconciler.mu.RLock()
		for trackedID, poolMap := range c.reconciler.tracked {
			if _, exists := currentPools[trackedID]; !exists {
				name := ""
				for _, r := range poolMap {
					name = r.PoolName
					break
				}
				removedPools = append(removedPools, removedPool{id: trackedID, name: name})
			}
		}
		c.reconciler.mu.RUnlock()
	}

	for _, removed := range removedPools {
		// Restart and out-of-band deletes converge gracefully: idle runners
		// are terminated now, busy runners finish their job under the
		// DefaultDrainBackstop (docs/25 §4.5). An explicit hard drain via
		// DeletePool supersedes this through the draining-set check.
		c.logger.Info("pool removed from database, draining gracefully", "pool", removed.name, "pool_id", removed.id)
		c.drainPool(ctx, removed.id, removed.name, 0, true)
	}

	// 6. Force-terminate busy runners whose job exceeds max_runner_lifetime_seconds (docs/03 §4, §7; docs/23)
	c.checkHungRunners(ctx, pools)

	// 7. Reconcile pool targets (converges up or down to live targets)
	for _, p := range pools {
		if err := c.reconcilePool(ctx, p); err != nil {
			c.logger.Error("failed reconciling pool target", "pool", p.Name, "err", err)
		}
	}

	c.mu.Lock()
	c.lastHeartbeat = time.Now()
	c.mu.Unlock()

	return nil
}

// CheckHungRunners inspects all pools and force-terminates busy runners whose
// job exceeds the pool's max_runner_lifetime_seconds, anchored at first busy
// assignment (docs/03 §4, §7; docs/23). Idle standbys are never terminated; the
// kill records a job_history 'timeout' row for the mid-job runner.
func (c *PoolController) CheckHungRunners(ctx context.Context) error {
	c.provisionMu.Lock()
	defer c.provisionMu.Unlock()

	pools, err := c.loadPools(ctx)
	if err != nil {
		return fmt.Errorf("loading pools for hung check: %w", err)
	}

	c.checkHungRunners(ctx, pools)
	return nil
}

func (c *PoolController) checkHungRunners(ctx context.Context, pools []db.RunnerPool) {
	if c.engine == nil || c.reconciler == nil {
		return
	}

	now := time.Now().UTC()
	for _, p := range pools {
		if p.MaxRunnerLifetimeSeconds <= 0 {
			continue
		}

		lifetimeLimit := time.Duration(p.MaxRunnerLifetimeSeconds) * time.Second

		// docs/21 §5.4: belt-and-braces — open rows older than 2x the lifetime
		// limit are crash leftovers; close them as 'interrupted'.
		if c.jobRecorder != nil {
			if n, err := c.jobRecorder.CloseStaleOpenJobs(ctx, p.ID, now.Add(-2*lifetimeLimit), now); err != nil {
				c.logger.Warn("closing stale open job rows", "pool", p.Name, "err", err)
			} else if n > 0 {
				c.logger.Info("closed stale open job rows", "pool", p.Name, "count", n)
			}
		}

		tracked := c.reconciler.TrackedPoolRunners(p.ID)
		for _, r := range tracked {
			if r.State != "running" || !r.IsBusy {
				continue // idle standbys are never lifetime-terminated (docs/23 §4.3)
			}

			// docs/23 §4.3: the lifetime clock is anchored at first busy
			// assignment, not spawn (BusySince, spawn-clock fallback docs/23 §4.4.1).
			anchor, anchorSrc, ok := c.busyAnchor(p.Name, r)
			if !ok {
				continue
			}

			if elapsed := now.Sub(anchor); elapsed > lifetimeLimit {
				c.logger.Warn("hung runner exceeded max lifetime, force terminating",
					"pool", p.Name,
					"runner_id", r.ID,
					"runner_name", r.Name,
					"elapsed", elapsed,
					"limit", lifetimeLimit,
					"anchor", anchor,
					"anchor_source", anchorSrc,
				)

				c.terminateHungRunner(ctx, p.ID, p.Name, r, anchor, now)
			}
		}
	}

	// Graceful-drain backstop (docs/25 §4.4): a deleted pool's leftover busy
	// runners keep a lifetime kill switch — the pool's max_runner_lifetime
	// when it had one, else DefaultDrainBackstop. Entries retire once the
	// pool's last container is gone.
	c.drainMu.Lock()
	draining := make(map[int64]drainEntry, len(c.drainingPools))
	for id, e := range c.drainingPools {
		draining[id] = e
	}
	c.drainMu.Unlock()

	for poolID, entry := range draining {
		tracked := c.reconciler.TrackedPoolRunners(poolID)
		alive := 0
		for _, r := range tracked {
			if r.State != "running" {
				continue
			}
			alive++
			if !r.IsBusy {
				continue // idle leftovers are not backstopped; none should exist post-drain
			}

			anchor, _, ok := c.busyAnchor(entry.name, r)
			if !ok {
				continue
			}

			if now.Sub(anchor) > entry.limit {
				c.logger.Warn("drained runner exceeded backstop, force terminating",
					"pool", entry.name,
					"pool_id", poolID,
					"runner_id", r.ID,
					"runner_name", r.Name,
					"elapsed", now.Sub(anchor),
					"limit", entry.limit,
					"anchor", anchor,
				)

				c.terminateHungRunner(ctx, poolID, entry.name, r, anchor, now)
			}
		}

		if alive == 0 {
			c.drainMu.Lock()
			delete(c.drainingPools, poolID)
			c.drainMu.Unlock()
			c.logger.Info("graceful drain complete", "pool", entry.name, "pool_id", poolID)
		}
	}
}

// busyAnchor returns the docs/23 lifetime clock for a busy runner: BusySince
// when set, else the spawn clock (docs/23 §4.4.1) so the guarantee never
// depends on anchor bookkeeping being complete. ok=false means no anchor at
// all — the caller must skip the runner.
func (c *PoolController) busyAnchor(poolName string, r RunnerStatus) (time.Time, string, bool) {
	anchor := r.BusySince
	src := "busy"
	if anchor.IsZero() {
		anchor, src = r.SpawnedAt, "spawn"
		if anchor.IsZero() {
			return time.Time{}, "", false
		}
		c.logger.Warn("busy runner without busy anchor, falling back to spawn time",
			"pool", poolName,
			"runner_id", r.ID,
			"runner_name", r.Name,
		)
	}
	return anchor, src, true
}

// terminateHungRunner force-terminates a busy runner that exceeded its
// lifetime limit — shared by the per-pool lifetime check (docs/23 §4.3) and
// the graceful-drain backstop (docs/25 §4.4). anchor is the docs/23 busy
// clock the elapsed time was measured from; job-history rows for deleted
// pools are already cascade-gone, making the timeout record a benign no-op
// (docs/25 §4.3).
func (c *PoolController) terminateHungRunner(ctx context.Context, poolID int64, poolName string, r RunnerStatus, anchor, now time.Time) {
	var logPath string
	if c.dataDir != "" {
		var err error
		logPath, err = c.engine.CaptureLogs(ctx, r.ID, c.dataDir)
		if err != nil {
			c.logger.Warn("capturing exit logs for hung runner", "id", r.ID, "err", err)
		}
	}

	// Force terminate container immediately.
	if err := c.engine.TerminateRunner(ctx, r.ID); err != nil {
		c.logger.Error("failed to force terminate hung runner", "id", r.ID, "err", err)
	}

	// Untrack runner from active pool state.
	c.reconciler.UntrackRunner(poolID, r.ID)

	// Record timeout in job_history (docs/21 §5.2, docs/23 §4.3): every
	// lifetime termination is mid-job (busy-only switch, docs/23 §4.3); the
	// row anchors at the same clock that fired the kill, not at spawn.
	if c.jobRecorder != nil {
		runnerName := r.Name
		if runnerName == "" {
			runnerName = r.ID
		}
		if err := c.jobRecorder.RecordJobTimeout(ctx, poolID, runnerName, logPath, anchor, now); err != nil {
			c.logger.Error("failed recording job timeout", "runner", runnerName, "err", err)
		}
	}

	// Capacity freed up, drain internal queue.
	c.drainQueue(ctx)
}

// HandleContainerEvent processes real-time Docker events ("die", "destroy").
// Reaps exited containers and immediately provisions a replacement idle runner (single-writer per docs/03 §1, §4).
func (c *PoolController) HandleContainerEvent(ctx context.Context, event ContainerEvent) error {
	c.mu.RLock()
	state := c.state
	c.mu.RUnlock()

	if state == StatePaused || state == StateStopped {
		return nil
	}

	if event.Action != "die" && event.Action != "destroy" {
		return nil
	}

	c.provisionMu.Lock()
	defer c.provisionMu.Unlock()

	c.logger.Info("handling container exit event", "pool", event.PoolName, "id", event.ContainerID, "action", event.Action)

	// Check if this was a one-off task container (e.g. Renovate bot per docs/03 §5)
	if c.taskExitHandler != nil {
		logPath := ""
		if c.dataDir != "" {
			logPath = LogPath(c.dataDir, event.ContainerID)
		}
		if handled, err := c.taskExitHandler.HandleContainerExit(ctx, event.ContainerID, event.ExitCode, logPath); handled {
			if err != nil {
				c.logger.Error("failed to handle task container exit", "id", event.ContainerID, "err", err)
			}
			if c.engine != nil {
				if err := c.engine.TerminateRunner(ctx, event.ContainerID); err != nil {
					c.logger.Warn("terminating exited task container", "id", event.ContainerID, "err", err)
				}
			}
			c.drainQueue(ctx)
			return nil
		}
	}

	poolID := event.PoolID
	if poolID == 0 && event.PoolName != "" {
		// Legacy container spawned before the pool-id label existed
		// (RUN-126): resolve its pool by the spawn-time name.
		poolID, _ = c.resolvePoolIDByName(event.PoolName)
	}

	// 1. Reap dead container
	c.reapContainer(ctx, event.ContainerID, poolID, event.ExitCode, true)

	// 2. Replenish target pool immediately
	if poolID != 0 {
		pools, err := c.loadPools(ctx)
		if err != nil {
			return fmt.Errorf("loading pools during event reap: %w", err)
		}
		c.refreshPoolIDs(pools)
		for _, p := range pools {
			if p.ID == poolID {
				return c.reconcilePool(ctx, p)
			}
		}
	}

	return nil
}

func (c *PoolController) reapContainer(ctx context.Context, containerID string, poolID int64, exitCode int, exitKnown bool) {
	c.closeJobRowOnDeath(ctx, containerID, poolID, exitCode, exitKnown)
	if c.dataDir != "" && c.engine != nil {
		if _, err := c.engine.CaptureLogs(ctx, containerID, c.dataDir); err != nil {
			// A concurrent reap path (audit cycle vs die/destroy event) already
			// captured the logs and started removal — benign (RUN-121).
			if errors.Is(err, ErrLogsUnavailable) {
				c.logger.Debug("skipping exit log capture, container already reaped by another path", "id", containerID, "err", err)
			} else {
				c.logger.Warn("capturing exit logs before container removal", "id", containerID, "err", err)
			}
		}
	}
	if c.engine != nil {
		if err := c.engine.TerminateRunner(ctx, containerID); err != nil {
			c.logger.Warn("terminating exited runner container", "id", containerID, "err", err)
		}
	}
	if c.reconciler != nil {
		c.reconciler.UntrackRunner(poolID, containerID)
	}

	// Drain internal provisioning queue as global capacity freed up
	c.drainQueue(ctx)
}

// closeJobRowOnDeath closes a runner's open job_history row when its container
// is reaped (docs/21 §5.2): a clean exit (code 0) closes the row as 'completed',
// anything else as 'interrupted' — the job outcome is unknowable without the
// forge API. Best-effort; boot recovery (docs/21 §5.4) catches any leftovers.
func (c *PoolController) closeJobRowOnDeath(ctx context.Context, containerID string, poolID int64, exitCode int, exitKnown bool) {
	if c.jobRecorder == nil || c.reconciler == nil || poolID == 0 {
		return
	}
	var runnerName string
	for _, r := range c.reconciler.TrackedPoolRunners(poolID) {
		if r.ID == containerID {
			runnerName = r.Name
			break
		}
	}
	if runnerName == "" {
		return
	}
	status := "interrupted"
	if exitKnown && exitCode == 0 {
		status = "completed"
	}
	if err := c.jobRecorder.CloseTransitionJob(ctx, poolID, runnerName, status, 0, "", time.Now().UTC()); err != nil {
		c.logger.Warn("closing job row on runner death", "pool_id", poolID, "runner", runnerName, "err", err)
	}
}

// Start boots the controller and runs the continuous periodic reconciliation loop until ctx is canceled.
func (c *PoolController) Start(ctx context.Context) error {
	if c.State() != StateRunning {
		if err := c.Boot(ctx); err != nil {
			return err
		}
	}

	if c.eventListener != nil {
		if c.dataDir != "" && c.engine != nil {
			c.eventListener.SetLogCapturer(func(ctx context.Context, id string) error {
				_, err := c.engine.CaptureLogs(ctx, id, c.dataDir)
				return err
			})
		}
		go func() {
			_ = c.eventListener.Listen(ctx, func(evt ContainerEvent) {
				_ = c.HandleContainerEvent(ctx, evt)
			})
		}()
	}

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.mu.Lock()
			c.state = StateStopped
			c.mu.Unlock()
			c.logger.Info("control loop terminated by context")
			return ctx.Err()

		case <-ticker.C:
			if err := c.Reconcile(ctx); err != nil {
				c.logger.Warn("reconciliation cycle error", "err", err)
			}
		}
	}
}

// ReadinessCheck adapts the control loop's heartbeat into a server.Check probe (OQ #19).
func (c *PoolController) ReadinessCheck() server.Check {
	return server.NewCheck("auditor", func(ctx context.Context) server.Status {
		c.mu.RLock()
		defer c.mu.RUnlock()

		if c.state == StateStopped {
			return server.StatusFail
		}
		if c.lastHeartbeat.IsZero() || time.Since(c.lastHeartbeat) > DefaultHeartbeatTimeout {
			return server.StatusDegraded
		}
		return server.StatusOK
	})
}

// GracefulShutdown executes the structured SIGTERM shutdown sequence (docs/03 §7, OQ #24):
// 1. Pauses the pool replenishing loop
// 2. Immediately deregisters and terminates all IDLE runners
// 3. Waits up to shutdownTimeout (polling every shutdownPollInterval) for ACTIVE runners to complete
// 4. Force-terminates any remaining containers if timeout expires
// 5. Exits cleanly with state StateStopped.
func (c *PoolController) GracefulShutdown(ctx context.Context) error {
	c.provisionMu.Lock()
	defer c.provisionMu.Unlock()

	c.Pause()
	c.logger.Info("initiated graceful shutdown protocol (SIGTERM)", "timeout", c.shutdownTimeout)

	if c.reconciler == nil || c.engine == nil {
		c.mu.Lock()
		c.state = StateStopped
		c.mu.Unlock()
		return nil
	}

	// 1. Terminate IDLE runners immediately; collect busy runners
	var activeRunners []RunnerStatus
	c.reconciler.mu.RLock()
	for _, poolMap := range c.reconciler.tracked {
		for _, r := range poolMap {
			if r.State != "running" {
				continue
			}
			if !r.IsBusy {
				c.logger.Info("terminating idle runner during graceful shutdown", "pool", r.PoolName, "id", r.ID, "name", r.Name)
				c.deregisterRunner(ctx, r)
				_ = c.engine.TerminateRunner(ctx, r.ID)
			} else {
				activeRunners = append(activeRunners, r)
			}
		}
	}
	c.reconciler.mu.RUnlock()

	// Clean untracked idle runners
	c.reconciler.mu.Lock()
	for _, poolMap := range c.reconciler.tracked {
		for id, r := range poolMap {
			if r.State == "running" && !r.IsBusy {
				delete(poolMap, id)
			}
		}
	}
	c.reconciler.mu.Unlock()

	if len(activeRunners) == 0 {
		c.logger.Info("no active runners remaining, graceful shutdown completed cleanly")
		c.mu.Lock()
		c.state = StateStopped
		c.mu.Unlock()
		return nil
	}

	c.logger.Info("waiting for active runners to complete", "count", len(activeRunners), "timeout", c.shutdownTimeout)

	// 2. Poll active containers every shutdownPollInterval up to shutdownTimeout
	timeoutChan := time.After(c.shutdownTimeout)
	ticker := time.NewTicker(c.shutdownPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.logger.Warn("context canceled during graceful shutdown, force-terminating remainder")
			c.forceTerminateRemaining(ctx)
			c.mu.Lock()
			c.state = StateStopped
			c.mu.Unlock()
			return ctx.Err()

		case <-timeoutChan:
			c.logger.Warn("shutdown timeout exceeded, force-terminating remaining active runners", "timeout", c.shutdownTimeout)
			c.forceTerminateRemaining(ctx)
			c.mu.Lock()
			c.state = StateStopped
			c.mu.Unlock()
			return nil

		case <-ticker.C:
			// Audit host containers to detect finished jobs
			report, err := c.reconciler.Audit(ctx)
			if err == nil {
				for _, exited := range report.Exited {
					c.reapContainer(ctx, exited.ID, exited.PoolID, exited.ExitCode, true)
				}
			}

			if c.TotalActiveRunners() == 0 {
				c.logger.Info("all active runners finished jobs cleanly, graceful shutdown complete")
				c.mu.Lock()
				c.state = StateStopped
				c.mu.Unlock()
				return nil
			}
			c.logger.Info("active runners still in progress", "remaining", c.TotalActiveRunners())
		}
	}
}

// ImmediateShutdown executes the immediate SIGINT (Ctrl+C) shutdown protocol (docs/03 §7):
// 1. Pauses the pool replenishing loop
// 2. Immediately deregisters and terminates all IDLE runners
// 3. Sends SIGTERM to all ACTIVE runner containers (Docker's 10s grace period applies)
// 4. Transitions controller to StateStopped.
func (c *PoolController) ImmediateShutdown(ctx context.Context) error {
	c.provisionMu.Lock()
	defer c.provisionMu.Unlock()

	c.Pause()
	c.logger.Info("initiated immediate shutdown protocol (SIGINT)")

	c.forceTerminateRemaining(ctx)

	c.mu.Lock()
	c.state = StateStopped
	c.mu.Unlock()

	c.logger.Info("immediate shutdown completed cleanly")
	return nil
}

func (c *PoolController) forceTerminateRemaining(ctx context.Context) {
	if c.reconciler == nil || c.engine == nil {
		return
	}
	c.reconciler.mu.Lock()
	defer c.reconciler.mu.Unlock()

	for poolName, poolMap := range c.reconciler.tracked {
		for id, r := range poolMap {
			if r.State == "running" {
				c.deregisterRunner(ctx, r)
				_ = c.engine.TerminateRunner(ctx, id)
				delete(poolMap, id)
			}
		}
		if len(poolMap) == 0 {
			delete(c.reconciler.tracked, poolName)
		}
	}
}

func (c *PoolController) deregisterRunner(ctx context.Context, r RunnerStatus) {
	if c.providerResolver == nil {
		return
	}
	pools, err := c.loadPools(ctx)
	if err != nil {
		return
	}
	for _, p := range pools {
		if p.Name == r.PoolName {
			gitProv, err := c.providerResolver.ResolveProvider(ctx, p.AuthProfileID)
			if err != nil {
				return
			}
			if dereg, ok := gitProv.(provider.RunnerDeregistrar); ok {
				runnerName := r.Name
				if runnerName == "" {
					runnerName = r.ID
				}
				targetURL := r.TargetURL
				if targetURL == "" {
					targetURL = p.RepositoryUrl
				}
				if err := dereg.DeregisterRunner(ctx, provider.RegistrationScope(p.Scope), targetURL, runnerName); err != nil {
					c.logger.Warn("failed to deregister runner via provider API", "runner", runnerName, "target", targetURL, "err", err)
				} else {
					c.logger.Info("successfully deregistered runner via provider API", "runner", runnerName, "target", targetURL)
				}
			}
			return
		}
	}
}

func (c *PoolController) loadPools(ctx context.Context) ([]db.RunnerPool, error) {
	if c.db == nil {
		return nil, nil
	}
	return c.db.ListRunnerPools(ctx)
}

func (c *PoolController) loadPoolTargets(ctx context.Context, p db.RunnerPool) []string {
	if tr, ok := c.db.(PoolTargetsRepository); ok {
		targets, err := tr.ListPoolTargetsByPoolId(ctx, p.ID)
		if err == nil && len(targets) > 0 {
			urls := make([]string, 0, len(targets))
			for _, t := range targets {
				if tURL := strings.TrimSpace(t.TargetUrl); tURL != "" {
					urls = append(urls, tURL)
				}
			}
			if len(urls) > 0 {
				return urls
			}
		}
	}
	if strings.TrimSpace(p.RepositoryUrl) != "" {
		return []string{strings.TrimSpace(p.RepositoryUrl)}
	}
	return nil
}

func (c *PoolController) validateAndProvisionPool(ctx context.Context, p db.RunnerPool) error {
	if c.providerResolver == nil {
		c.setPoolError(p.Name, p.ID, ErrCodeProviderAuthFailed, "No Git provider resolver configured")
		return nil
	}

	gitProv, err := c.providerResolver.ResolveProvider(ctx, p.AuthProfileID)
	if err != nil {
		code, msg := ClassifyReconcileError(err)
		c.setPoolError(p.Name, p.ID, code, msg)
		return fmt.Errorf("resolving provider for pool %q: %w", p.Name, err)
	}

	if err := gitProv.ValidateCredentials(ctx); err != nil {
		code, msg := ClassifyReconcileError(err)
		c.setPoolError(p.Name, p.ID, code, msg)
		return fmt.Errorf("validating provider credentials for pool %q: %w", p.Name, err)
	}

	return c.reconcilePoolWithProvider(ctx, p, gitProv)
}

// fetchRemoteRunners lists registered runners for the pool from the first
// successful target (docs/19 §2.3, docs/20 §4.2). Returns a nil listing when
// the provider lacks RunnerLister support, no targets are configured, or
// every target fails — callers treat nil as "no data this cycle" (fail-open).
// The resolved target is returned alongside so consumers act on the same
// scope the listing came from.
func (c *PoolController) fetchRemoteRunners(ctx context.Context, gitProv provider.GitProvider, p db.RunnerPool) ([]provider.RemoteRunnerStatus, string) {
	if gitProv == nil {
		return nil, ""
	}
	lister, ok := gitProv.(provider.RunnerLister)
	if !ok {
		return nil, ""
	}

	targets := c.loadPoolTargets(ctx, p)
	if len(targets) == 0 {
		return nil, ""
	}

	scope := provider.RegistrationScope(p.Scope)
	for _, target := range targets {
		remote, err := lister.ListRunners(ctx, scope, target)
		if err != nil {
			c.logger.Warn("listing registered runners failed",
				"pool", p.Name, "target", target, "err", err)
			continue
		}
		return remote, target
	}
	return nil, ""
}

// applyRemoteBusyState converges tracked runners' IsBusy flags with the
// provider's registered-runner state (docs/19 §2.3). The forge API is the
// authoritative source; workflow_job webhooks remain the sub-second fast
// path. Best-effort by design: nil listings and per-runner guards are
// skipped without blocking the reconcile (fail-open).
//
// The same transitions drive job-history recording (docs/21 §5.2):
// idle→busy opens a job row, busy→idle closes it.
func (c *PoolController) applyRemoteBusyState(ctx context.Context, p db.RunnerPool, gitProv provider.GitProvider, remote []provider.RemoteRunnerStatus) {
	if c.reconciler == nil || len(remote) == 0 {
		return
	}

	tracked := c.reconciler.TrackedPoolRunners(p.ID)
	if len(tracked) == 0 {
		return
	}

	remoteByName := make(map[string]provider.RemoteRunnerStatus, len(remote))
	for _, r := range remote {
		remoteByName[r.Name] = r
	}

	for _, r := range tracked {
		state, found := remoteByName[r.Name]
		if !found {
			// Absent from the listing: leave state untouched — the
			// deregistration lifecycle owns runner removal (docs/19 §2.3).
			continue
		}
		if !state.Online {
			// Offline guard: preserve current state against
			// registration/contact races (docs/19 §2.3).
			continue
		}
		// Persist the forge-assigned runner id for conclusion enrichment
		// (docs/21 §5.3): the listing is the only id source, so each
		// observation refreshes the tracked state.
		if state.ID > 0 && state.ID != r.ForgeID {
			c.reconciler.SetRunnerForgeID(r.Name, state.ID)
		}
		if r.IsBusy != state.Busy {
			c.reconciler.MarkRunnerBusy(r.Name, state.Busy)
			if state.Busy {
				c.recordJobTransition(ctx, p, r.Name, true)
			} else {
				// Job completion (docs/21 §5.3): prefer the forge's
				// conclusion and external job id, falling back to a plain
				// completed row when enrichment is disabled, unsupported,
				// or fails (G3).
				forgeID := r.ForgeID
				if state.ID > 0 {
					forgeID = state.ID
				}
				status, jobID := c.enrichedCloseStatus(ctx, gitProv, p, forgeID)
				c.recordJobClose(ctx, p, r.Name, status, jobID)
			}
		}
	}
}

// recordJobTransition opens a job-history row for a runner observed flipping
// idle to busy (docs/21 §5.2). Best-effort: recording failures are logged and
// never block the reconcile cycle (docs/21 G3).
func (c *PoolController) recordJobTransition(ctx context.Context, p db.RunnerPool, runnerName string, busy bool) {
	if c.jobRecorder == nil {
		return
	}
	if err := c.jobRecorder.OpenTransitionJob(ctx, p.ID, runnerName, time.Now().UTC()); err != nil {
		c.logger.Warn("job lifecycle recording failed", "pool", p.Name, "runner", runnerName, "busy", busy, "err", err)
	}
}

// recordJobClose closes a runner's open job-history row with the given terminal
// status, enriching it with the forge's external job id when known (docs/21
// §5.2, §5.3). Best-effort per G3.
func (c *PoolController) recordJobClose(ctx context.Context, p db.RunnerPool, runnerName, status string, jobID int64) {
	if c.jobRecorder == nil {
		return
	}
	if err := c.jobRecorder.CloseTransitionJob(ctx, p.ID, runnerName, status, jobID, "", time.Now().UTC()); err != nil {
		c.logger.Warn("job lifecycle recording failed", "pool", p.Name, "runner", runnerName, "status", status, "err", err)
	}
}

// conclusionEnrichTimeout bounds the single per-completion forge call (docs/21
// §5.3): enrichment must never stall the reconcile loop.
const conclusionEnrichTimeout = 5 * time.Second

// enrichedCloseStatus resolves the terminal status and external job id for a
// just-completed job by asking the forge for the runner's latest jobs once
// (docs/21 §5.3). Fail-open contract (G3): disabled via config, missing forge
// id, missing RunnerJobsLister capability, unsupported scope, API error, or no
// concluded job all degrade to a plain completed row with no job id.
func (c *PoolController) enrichedCloseStatus(ctx context.Context, gitProv provider.GitProvider, p db.RunnerPool, forgeID int64) (string, int64) {
	if !c.enrichConclusions || forgeID <= 0 || gitProv == nil {
		return "completed", 0
	}
	lister, ok := gitProv.(provider.RunnerJobsLister)
	if !ok {
		return "completed", 0
	}

	ctx, cancel := context.WithTimeout(ctx, conclusionEnrichTimeout)
	defer cancel()

	jobs, err := lister.RunnerLatestJobs(ctx, provider.RegistrationScope(p.Scope), p.RepositoryUrl, forgeID)
	if err != nil {
		c.logger.Warn("conclusion enrichment failed, closing as completed", "pool", p.Name, "forge_id", forgeID, "err", err)
		return "completed", 0
	}
	for _, j := range jobs {
		// Newest-first: the first job the forge reports as concluded is the
		// one that just finished; running entries (empty completed_at) are
		// skipped rather than trusted, since busy-flag lag can race the
		// forge's own job state.
		if j.CompletedAt.IsZero() {
			continue
		}
		return webhookConclusionStatus(j.Conclusion), j.ID
	}
	return "completed", 0
}

// ghostSweep deregisters orphaned runner registrations — idle, offline,
// locally-untracked runnero-* entries observed offline-untracked for
// ghostOfflineCycles consecutive audit cycles (docs/20 §4.1). Ghosts arise
// when containers die ungracefully (OOM kills, docker kill, host power
// loss), bypassing --ephemeral cleanup and the entrypoint's signal trap.
// Purely API-side reconciliation: busy, tracked, and foreign (non-runnero)
// registrations are never touched; failures fail open and retry naturally
// on the next cycle. The counters map is guarded by ghostMu for the whole
// body: reconcilePool can run concurrently (control loop + die-event
// handler), and a network deregistration under lock matches the existing
// drain-path precedent.
func (c *PoolController) ghostSweep(ctx context.Context, gitProv provider.GitProvider, p db.RunnerPool, target string, remote []provider.RemoteRunnerStatus) {
	if c.reconciler == nil || len(remote) == 0 || target == "" || c.ghostOfflineCycles <= 0 {
		return
	}
	dereg, ok := gitProv.(provider.RunnerDeregistrar)
	if !ok {
		return
	}

	trackedNames := make(map[string]struct{})
	for _, r := range c.reconciler.TrackedPoolRunners(p.ID) {
		trackedNames[r.Name] = struct{}{}
	}
	prefix := "runnero-" + SlugifyPoolName(p.Name) + "-"

	c.ghostMu.Lock()
	defer c.ghostMu.Unlock()

	counters := c.ghostCounters[p.ID]
	if counters == nil {
		counters = make(map[string]int)
		c.ghostCounters[p.ID] = counters
	}

	seen := make(map[string]struct{}, len(remote))
	swept := 0
	for _, rr := range remote {
		seen[rr.Name] = struct{}{}
		if rr.Online || rr.Busy || !strings.HasPrefix(rr.Name, prefix) {
			delete(counters, rr.Name)
			continue
		}
		if _, isTracked := trackedNames[rr.Name]; isTracked {
			// Tracked runners are owned by drain logic and the M23 offline
			// guard (docs/19 §2.3) — never swept here.
			delete(counters, rr.Name)
			continue
		}

		counters[rr.Name]++
		if counters[rr.Name] < c.ghostOfflineCycles {
			continue
		}
		if swept >= c.ghostMaxDeregistrations {
			// Burst guard: counter keeps growing, remainder swept next cycle.
			continue
		}
		if err := dereg.DeregisterRunner(ctx, provider.RegistrationScope(p.Scope), target, rr.Name); err != nil {
			c.logger.Warn("ghost sweep deregistration failed",
				"pool", p.Name, "runner", rr.Name, "target", target, "err", err)
			continue // counter persists; retried next cycle
		}
		delete(counters, rr.Name)
		swept++
		c.logger.Info("ghost sweep deregistered orphaned runner",
			"pool", p.Name, "runner", rr.Name, "target", target,
			"offlineCycles", c.ghostOfflineCycles)
	}

	// Drop counters for names no longer in the listing (deregistered
	// server-side or self-removed) so a re-registration starts fresh.
	for name := range counters {
		if _, stillListed := seen[name]; !stillListed {
			delete(counters, name)
		}
	}
	if len(counters) == 0 {
		delete(c.ghostCounters, p.ID)
	}
}

func (c *PoolController) reconcilePool(ctx context.Context, p db.RunnerPool) error {
	if c.providerResolver == nil {
		c.setPoolError(p.Name, p.ID, ErrCodeProviderAuthFailed, "No Git provider resolver configured")
		return nil
	}
	gitProv, err := c.providerResolver.ResolveProvider(ctx, p.AuthProfileID)
	if err != nil {
		code, msg := ClassifyReconcileError(err)
		c.setPoolError(p.Name, p.ID, code, msg)
		return fmt.Errorf("resolving provider: %w", err)
	}
	return c.reconcilePoolWithProvider(ctx, p, gitProv)
}

func (c *PoolController) reconcilePoolWithProvider(ctx context.Context, p db.RunnerPool, gitProv provider.GitProvider) error {
	if c.engine == nil || c.reconciler == nil {
		return nil
	}

	// Fetch the provider's registered-runner listing once per cycle and feed
	// both consumers: busy-state convergence (docs/19 §2.3) and the ghost
	// sweep (docs/20 §4). This runs BEFORE the classification below feeds
	// scaling decisions, so missed webhook events cannot cause mid-job drains
	// or phantom idle, and ungraceful container deaths cannot leave orphaned
	// registrations behind. Webhooks remain the sub-second fast path.
	remote, target := c.fetchRemoteRunners(ctx, gitProv, p)
	c.applyRemoteBusyState(ctx, p, gitProv, remote)
	c.ghostSweep(ctx, gitProv, p, target, remote)

	tracked := c.reconciler.TrackedPoolRunners(p.ID)
	var idleRunners []RunnerStatus
	activeCount := int64(0)
	for _, r := range tracked {
		if r.State == "running" {
			activeCount++
			if !r.IsBusy {
				idleRunners = append(idleRunners, r)
			}
		}
	}

	targets := c.loadPoolTargets(ctx, p)
	if len(targets) == 0 {
		c.setPoolError(p.Name, p.ID, ErrCodeTargetNotFound, "No target repository or organization configured")
		return nil
	}

	effectiveTarget := p.MinIdleRunners

	// demandQueue holds demand-directed spawn targets (RUN-151): one entry per
	// queued job that no idle runner on that same target can cover, in stable
	// targets-slice order. Empty unless this cycle polled demand.
	var demandQueue []string

	// Polling-based scaling for providers without webhook support (e.g. Forgejo per docs/03 §3b, RUN-70):
	// Demand polling (docs/24 §5.1): natively polling providers (Forgejo) always
	// poll; webhook providers poll only when the pool opts in via poll_fallback.
	pollsDemand := gitProv != nil && (p.PollFallback || gitProv.ScalingMode() == provider.ScalingPolling)
	if pollsDemand && c.pollDue(p) {
		totalQueued := 0
		note := ""
		pollFailures := 0
		demandByTarget := make(map[string]int, len(targets))
		for _, target := range targets {
			pollTarget := provider.PollTarget{
				URL:    target,
				Scope:  provider.RegistrationScope(p.Scope),
				Labels: p.Labels,
			}
			queuedJobs, err := gitProv.PollQueuedJobs(ctx, pollTarget)
			switch {
			case errors.Is(err, provider.ErrPollingScopeUnsupported):
				// Org/global targets keep webhook-only demand (docs/24 §5.2):
				// skip with a diagnostic note, never an error-level failure.
				note = fmt.Sprintf("poll skipped: no org-level queued-jobs API for %s", target)
				c.logger.Info("demand polling skipped unsupported target scope",
					"pool", p.Name, "target", target, "scope", p.Scope)
				continue
			case errors.Is(err, provider.ErrPollingUnsupported):
				note = fmt.Sprintf("poll unsupported: %v", err)
				c.logger.Warn("demand polling unsupported for provider", "pool", p.Name, "err", err)
				continue
			case err != nil:
				pollFailures++
				c.logger.Warn("polling queued jobs for target failed", "pool", p.Name, "target", target, "err", err)
				continue
			}
			demandByTarget[target] = queuedJobs
			totalQueued += queuedJobs
		}
		c.recordPollOutcome(p.Name, p.ID, totalQueued, pollFailures, len(targets), note)

		// RUN-151: attribute idle runners to the target they registered against,
		// so demand on one repo is not masked by idle runners on another, and
		// deficit spawns are directed at the repo that actually has queued jobs.
		idleByTarget := make(map[string]int64, len(idleRunners))
		for _, r := range idleRunners {
			if r.TargetURL != "" {
				idleByTarget[r.TargetURL]++
			}
		}

		idleCount := int64(len(idleRunners))
		totalDeficit := int64(0)
		for _, target := range targets {
			deficit := int64(demandByTarget[target]) - idleByTarget[target]
			if deficit <= 0 {
				continue
			}
			totalDeficit += deficit
			for i := int64(0); i < deficit; i++ {
				demandQueue = append(demandQueue, target)
			}
		}
		if totalDeficit > 0 {
			c.logger.Info("polling detected queued jobs exceeding idle runners",
				"pool", p.Name,
				"queued_jobs", totalQueued,
				"idle_runners", idleCount,
				"additional_needed", totalDeficit,
				"demand_by_target", demandByTarget,
			)
			if activeCount+totalDeficit > effectiveTarget {
				effectiveTarget = activeCount + totalDeficit
			}
		}
	}

	if p.MaxConcurrency > 0 && effectiveTarget > p.MaxConcurrency {
		effectiveTarget = p.MaxConcurrency
	}

	// 1. Enforce per-pool MaxConcurrency cap if active runners exceed it (e.g. reduced live)
	if p.MaxConcurrency > 0 && activeCount > p.MaxConcurrency {
		excess := activeCount - p.MaxConcurrency
		c.logger.Info("pool active runners exceed max_concurrency, draining idle runners",
			"pool", p.Name, "active", activeCount, "max_concurrency", p.MaxConcurrency, "excess", excess)
		drained := int64(0)
		var remainingIdle []RunnerStatus
		for _, r := range idleRunners {
			if drained < excess {
				c.deregisterRunner(ctx, r)
				_ = c.engine.TerminateRunner(ctx, r.ID)
				c.reconciler.UntrackRunner(p.ID, r.ID)
				drained++
			} else {
				remainingIdle = append(remainingIdle, r)
			}
		}
		activeCount -= drained
		idleRunners = remainingIdle
	}

	// 2. Scale down idle runners according to pool scaling mode:
	// - Scale-to-zero mode (MinIdleRunners == 0): idle standby runners are drained immediately,
	//   while on-demand runners are preserved during their startup grace period so they can accept queued jobs (RUN-71).
	//   Orphaned on-demand runners that exceed the grace period without picking up a job are drained.
	//   The lifetime switch never caps the grace period: it is busy-only since
	//   docs/23 §4.6, so unpicked on-demand runners are governed by the grace
	//   period alone.
	// - Fixed idle target (MinIdleRunners > 0): excess idle runners beyond target are drained (RUN-42),
	//   but on-demand runners are spared during the same startup grace period (RUN-156).
	if p.MinIdleRunners == 0 {
		gracePeriod := c.scaleToZeroGracePeriod
		now := time.Now().UTC()
		for _, r := range idleRunners {
			isStaleStandby := !r.OnDemand
			isStaleOnDemand := r.OnDemand && (!r.SpawnedAt.IsZero() && now.Sub(r.SpawnedAt) >= gracePeriod)
			if isStaleStandby || isStaleOnDemand {
				c.logger.Info("scale-to-zero draining idle runner",
					"pool", p.Name,
					"runner", r.ID,
					"on_demand", r.OnDemand,
					"stale_standby", isStaleStandby,
					"stale_on_demand", isStaleOnDemand,
				)
				c.deregisterRunner(ctx, r)
				_ = c.engine.TerminateRunner(ctx, r.ID)
				c.reconciler.UntrackRunner(p.ID, r.ID)
				activeCount--
			}
		}
	} else if int64(len(idleRunners)) > effectiveTarget {
		excess := int64(len(idleRunners)) - effectiveTarget
		c.logger.Info("pool idle runners exceed target, draining excess idle runners",
			"pool", p.Name, "idle_count", len(idleRunners), "target", effectiveTarget, "excess", excess)
		// RUN-156: on-demand runners inside their startup grace period are spared —
		// they may still be registering with the provider to pick up a queued job
		// (same guarantee as the scale-to-zero branch above, RUN-71). Without this,
		// a reconcile tick landing between a demand spawn and its first job pickup
		// kills the runner before it can go busy. On-demand runners with unknown
		// age (zero SpawnedAt) drain as before, matching legacy behavior.
		gracePeriod := c.scaleToZeroGracePeriod
		now := time.Now().UTC()
		drained := int64(0)
		for _, r := range idleRunners {
			if drained >= excess {
				break
			}
			if r.OnDemand && !r.SpawnedAt.IsZero() && now.Sub(r.SpawnedAt) < gracePeriod {
				continue
			}
			c.deregisterRunner(ctx, r)
			_ = c.engine.TerminateRunner(ctx, r.ID)
			c.reconciler.UntrackRunner(p.ID, r.ID)
			drained++
			activeCount--
		}
	}

	queuedForPool := int64(c.QueueLengthForPool(p.ID))
	needed := effectiveTarget - (activeCount + queuedForPool)
	if needed <= 0 {
		intent := ""
		if p.MinIdleRunners == 0 {
			intent = fmt.Sprintf("Scale-to-zero active (%d active runner(s))", activeCount)
		} else {
			intent = fmt.Sprintf("Warm pool satisfied (%d/%d idle runner(s))", len(idleRunners), p.MinIdleRunners)
		}
		c.setPoolHealthy(p.Name, p.ID, intent)
		return nil
	}

	c.setPoolProvisioning(p.Name, p.ID, fmt.Sprintf("Launching %d runner(s) (target idle: %d, current idle: %d)", needed, effectiveTarget, len(idleRunners)))

	c.logger.Info("reconciling idle runners for pool",
		"pool", p.Name,
		"needed", needed,
		"active", activeCount,
		"queued", queuedForPool,
		"target", effectiveTarget,
		"max_concurrency", p.MaxConcurrency,
	)

	onDemand := (p.MinIdleRunners == 0 || pollsDemand)
	for i := int64(0); i < needed; i++ {
		// Check per-pool max_concurrency
		if p.MaxConcurrency > 0 && (activeCount+int64(c.QueueLengthForPool(p.ID))) >= p.MaxConcurrency {
			c.logger.Info("pool reached max concurrency limit, skipping further spawns", "pool", p.Name, "max_concurrency", p.MaxConcurrency)
			break
		}

		// RUN-151: honor demand-directed targets first (queued jobs on a specific
		// repo); fall back to round-robin for plain min-idle replenishment.
		targetURL := ""
		if len(demandQueue) > 0 {
			targetURL = demandQueue[0]
			demandQueue = demandQueue[1:]
		} else {
			targetURL = targets[int(activeCount)%len(targets)]
		}

		// Check global quota circuit breaker (Total Allowed Runners per docs/03 §4, docs/05 §3)
		if c.globalMaxRunners > 0 && c.TotalActiveRunners() >= c.globalMaxRunners {
			c.logger.Warn("global runner quota saturated, queuing provisioning request internally",
				"pool", p.Name,
				"global_active", c.TotalActiveRunners(),
				"global_max", c.globalMaxRunners,
			)
			c.setPoolError(p.Name, p.ID, ErrCodeGlobalQuotaSaturated, fmt.Sprintf("Global runner quota saturated (%d/%d): queued provisioning request", c.TotalActiveRunners(), c.globalMaxRunners))
			c.enqueueRequest(p, targetURL)
			continue
		}

		if err := c.spawnSingleRunner(ctx, p, gitProv, onDemand, targetURL); err != nil {
			code, msg := ClassifyReconcileError(err)
			c.setPoolError(p.Name, p.ID, code, msg)
			return err
		}
		activeCount++
	}

	intent := ""
	if p.MinIdleRunners == 0 {
		intent = fmt.Sprintf("Scale-to-zero active (%d active runner(s))", activeCount)
	} else {
		intent = fmt.Sprintf("Warm pool satisfied (%d/%d idle runner(s))", len(idleRunners)+int(needed), p.MinIdleRunners)
	}
	c.setPoolHealthy(p.Name, p.ID, intent)
	return nil
}

func (c *PoolController) spawnSingleRunner(ctx context.Context, p db.RunnerPool, gitProv provider.GitProvider, onDemand bool, targetURL string) error {
	if gitProv == nil {
		var err error
		gitProv, err = c.providerResolver.ResolveProvider(ctx, p.AuthProfileID)
		if err != nil {
			return fmt.Errorf("resolving provider for pool %q: %w", p.Name, err)
		}
	}

	if targetURL == "" {
		targetURL = p.RepositoryUrl
	}

	token, err := gitProv.GetRegistrationToken(ctx, provider.RegistrationScope(p.Scope), targetURL)
	if err != nil {
		return fmt.Errorf("getting registration token for pool %q target %q: %w", p.Name, targetURL, err)
	}

	containerName := GenerateContainerName(p.Name)
	labels := formatLabels(p.Labels)

	env := []string{
		"GITHUB_REPOSITORY_URL=" + targetURL,
		"RUNNER_TOKEN=" + token,
		"RUNNER_NAME=" + containerName,
		"RUNNER_LABELS=" + labels,
		"RUNNER_WORKDIR=_work",
		"RUNNER_EPHEMERAL=1",
	}
	if strings.Contains(strings.ToLower(p.Provider), "gitea") {
		env = append(env, "GITEA_INSTANCE_URL="+targetURL)
	} else if strings.Contains(strings.ToLower(p.Provider), "forgejo") {
		env = append(env, "FORGEJO_INSTANCE_URL="+targetURL)
	}

	config := RunnerConfig{
		Name:        containerName,
		PoolName:    p.Name,
		PoolID:      p.ID,
		RepoURL:     targetURL,
		Token:       token,
		Image:       p.RunnerImage,
		AllowDocker: p.AllowDocker,
		Env:         env,
	}
	if p.CpuLimit.Valid {
		config.CPULimit = p.CpuLimit.String
	}
	if p.MemoryLimit.Valid {
		config.MemoryLimit = p.MemoryLimit.String
	}

	id, err := c.engine.SpawnRunner(ctx, config)
	if err != nil {
		return fmt.Errorf("spawning runner for pool %q: %w", p.Name, err)
	}

	c.reconciler.TrackRunner(RunnerStatus{
		ID:        id,
		Name:      containerName,
		PoolName:  p.Name,
		PoolID:    p.ID,
		State:     "running",
		SpawnedAt: time.Now().UTC(),
		OnDemand:  onDemand,
		TargetURL: targetURL,
	})

	if onDemand {
		c.logger.Info("spawned on-demand runner", "pool", p.Name, "id", id, "target", targetURL)
	} else {
		c.logger.Info("spawned standby runner", "pool", p.Name, "id", id, "target", targetURL)
	}
	return nil
}

// TotalActiveRunners returns the total count of running runner containers across all pools.
func (c *PoolController) TotalActiveRunners() int {
	if c.reconciler == nil {
		return 0
	}
	c.reconciler.mu.RLock()
	defer c.reconciler.mu.RUnlock()

	count := 0
	for _, poolMap := range c.reconciler.tracked {
		for _, r := range poolMap {
			if r.State == "running" {
				count++
			}
		}
	}
	return count
}

// PoolStats returns the active (busy running a job) and idle runner counts for a pool.
func (c *PoolController) PoolStats(poolID int64) (active int32, idle int32) {
	if c.reconciler == nil {
		return 0, 0
	}
	c.reconciler.mu.RLock()
	defer c.reconciler.mu.RUnlock()

	poolMap, ok := c.reconciler.tracked[poolID]
	if !ok {
		return 0, 0
	}
	for _, r := range poolMap {
		if r.State == "running" {
			if r.IsBusy {
				active++
			} else {
				idle++
			}
		}
	}
	return active, idle
}

// PoolDiagnostics returns the live operational health, intent, and reconciliation diagnostics for a pool.
func (c *PoolController) PoolDiagnostics(poolID int64) server.PoolDiagnostics {
	c.diagMu.RLock()
	defer c.diagMu.RUnlock()

	diag, ok := c.diagnostics[poolID]
	if !ok {
		return server.PoolDiagnostics{
			HealthStatus:  string(HealthHealthy),
			CurrentIntent: "Ready",
		}
	}
	return server.PoolDiagnostics{
		HealthStatus:        string(diag.HealthStatus),
		CurrentIntent:       diag.CurrentIntent,
		LastError:           diag.LastError,
		LastErrorCode:       diag.LastErrorCode,
		LastErrorTimestamp:  diag.LastErrorTimestamp,
		LastReconciledAt:    diag.LastReconciledAt,
		LastPollAt:          diag.LastPollAt,
		LastPollQueuedCount: diag.LastPollQueuedCount,
		LastPollError:       diag.LastPollError,
	}
}

func (c *PoolController) setPoolProvisioning(poolName string, poolID int64, intent string) {
	c.diagMu.Lock()
	defer c.diagMu.Unlock()

	diag, ok := c.diagnostics[poolID]
	if !ok {
		diag = PoolDiagnosticState{
			PoolID:   poolID,
			PoolName: poolName,
		}
	}
	diag.HealthStatus = HealthProvisioning
	diag.CurrentIntent = intent
	diag.LastReconciledAt = time.Now().UTC()
	c.diagnostics[poolID] = diag
}

func (c *PoolController) setPoolHealthy(poolName string, poolID int64, intent string) {
	c.diagMu.Lock()
	defer c.diagMu.Unlock()

	diag, ok := c.diagnostics[poolID]
	if !ok {
		diag = PoolDiagnosticState{
			PoolID:   poolID,
			PoolName: poolName,
		}
	}
	diag.HealthStatus = HealthHealthy
	diag.CurrentIntent = intent
	diag.LastError = ""
	diag.LastErrorCode = ""
	diag.LastReconciledAt = time.Now().UTC()
	c.diagnostics[poolID] = diag
}

func (c *PoolController) setPoolError(poolName string, poolID int64, code, msg string) {
	c.diagMu.Lock()
	defer c.diagMu.Unlock()

	diag, ok := c.diagnostics[poolID]
	if !ok {
		diag = PoolDiagnosticState{
			PoolID:   poolID,
			PoolName: poolName,
		}
	}
	diag.HealthStatus = HealthDegraded
	diag.LastErrorCode = code
	diag.LastError = msg
	diag.LastErrorTimestamp = time.Now().UTC()
	diag.LastReconciledAt = time.Now().UTC()
	c.diagnostics[poolID] = diag
}

func (c *PoolController) removePoolDiagnostics(poolID int64) {
	c.diagMu.Lock()
	defer c.diagMu.Unlock()
	delete(c.diagnostics, poolID)
}

// SystemRunnerStats returns the total active (busy executing job) and idle runner counts across all pools.
func (c *PoolController) SystemRunnerStats() (active int32, idle int32) {
	if c.reconciler == nil {
		return 0, 0
	}
	c.reconciler.mu.RLock()
	defer c.reconciler.mu.RUnlock()

	for _, poolMap := range c.reconciler.tracked {
		for _, r := range poolMap {
			if r.State == "running" {
				if r.IsBusy {
					active++
				} else {
					idle++
				}
			}
		}
	}
	return active, idle
}

// QueueLength returns the number of currently queued provisioning requests.
func (c *PoolController) QueueLength() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.queue)
}

// QueueLengthForPool returns the number of queued requests for a specific pool.
func (c *PoolController) QueueLengthForPool(poolID int64) int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	count := 0
	for _, req := range c.queue {
		if req.PoolID == poolID {
			count++
		}
	}
	return count
}

func (c *PoolController) enqueueRequest(pool db.RunnerPool, targetURL string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.queue = append(c.queue, ProvisionRequest{
		PoolID:    pool.ID,
		PoolName:  pool.Name,
		TargetURL: targetURL,
		CreatedAt: time.Now().UTC(),
	})
}

// drainQueue processes queued requests fairly when global capacity becomes available (docs/03 §4).
func (c *PoolController) drainQueue(ctx context.Context) {
	c.mu.Lock()
	if len(c.queue) == 0 {
		c.mu.Unlock()
		return
	}
	queueCopy := append([]ProvisionRequest(nil), c.queue...)
	c.mu.Unlock()

	pools, err := c.loadPools(ctx)
	if err != nil {
		return
	}
	poolMap := make(map[string]db.RunnerPool, len(pools))
	for _, p := range pools {
		poolMap[p.Name] = p
	}

	var remaining []ProvisionRequest
	for _, req := range queueCopy {
		// Stop if global quota is saturated
		if c.globalMaxRunners > 0 && c.TotalActiveRunners() >= c.globalMaxRunners {
			remaining = append(remaining, req)
			continue
		}

		p, exists := poolMap[req.PoolName]
		if !exists {
			// Pool no longer exists, discard request
			continue
		}

		poolActive := int64(0)
		for _, r := range c.reconciler.TrackedPoolRunners(p.ID) {
			if r.State == "running" {
				poolActive++
			}
		}

		// Respect per-pool max_concurrency
		if p.MaxConcurrency > 0 && poolActive >= p.MaxConcurrency {
			remaining = append(remaining, req)
			continue
		}

		targetURL := req.TargetURL
		if targetURL == "" {
			targets := c.loadPoolTargets(ctx, p)
			if len(targets) > 0 {
				targetURL = targets[int(poolActive)%len(targets)]
			} else {
				targetURL = p.RepositoryUrl
			}
		}

		if err := c.spawnSingleRunner(ctx, p, nil, true, targetURL); err != nil {
			c.logger.Error("failed spawning queued runner", "pool", p.Name, "err", err)
			remaining = append(remaining, req)
			continue
		}
	}

	c.mu.Lock()
	c.queue = remaining
	c.mu.Unlock()
}

func formatLabels(raw string) string {
	if raw == "" {
		return "self-hosted,linux"
	}
	var arr []string
	if err := json.Unmarshal([]byte(raw), &arr); err == nil && len(arr) > 0 {
		return strings.Join(arr, ",")
	}
	return raw
}

// Reload immediately reloads pool definitions and application settings from the database at runtime,
// converging active pools up or down to new min_idle/max_concurrency targets and draining deleted pools (docs/02 §4, docs/03 §4).
func (c *PoolController) Reload(ctx context.Context) error {
	c.logger.Info("reloading pool configurations and settings from database")
	return c.Reconcile(ctx)
}

// drainEntry records a pool being gracefully drained: the deleted pool's
// lifetime switch (or DefaultDrainBackstop) applied to its leftover busy
// runners (docs/25 §4.2, §4.4).
type drainEntry struct {
	name  string
	limit time.Duration
}

// DrainPool tears down a deleted pool's runners after the pool row is gone
// (docs/25 §4.2). Hard mode (graceful=false) is the pre-RUN-127 behavior:
// every running runner is deregistered, terminated, and untracked. Graceful
// mode terminates idle runners immediately and leaves busy runners to finish
// their current job — the ephemeral runner exits, the container exits, and
// the generic audit reap path cleans it up (docs/25 §4.3); a lifetime
// backstop bounds hung leftovers (docs/25 §4.4). DeletePool calls this
// directly; the reconciler's removed-pool detection reaches it through the
// graceful fallback (docs/25 §4.5).
func (c *PoolController) DrainPool(ctx context.Context, poolID int64, poolName string, lifetime time.Duration, graceful bool) {
	c.drainPool(ctx, poolID, poolName, lifetime, graceful)
}

var _ server.PoolDrainer = (*PoolController)(nil)

// drainPool implements DrainPool. Graceful drains are idempotent per pool:
// once recorded in the draining set, repeat calls (RPC and removed-pool
// fallback alike) return early; checkHungRunners retires the entry when the
// pool's last runner is gone.
func (c *PoolController) drainPool(ctx context.Context, poolID int64, poolName string, lifetime time.Duration, graceful bool) {
	if c.reconciler == nil || c.engine == nil {
		return
	}

	if poolName == "" {
		poolName = fmt.Sprintf("pool-%d", poolID)
	}

	if graceful {
		c.drainMu.Lock()
		if _, draining := c.drainingPools[poolID]; draining {
			c.drainMu.Unlock()
			return
		}
		c.drainMu.Unlock()
	} else {
		// A hard drain supersedes any in-flight graceful drain: everything
		// is terminated now and nothing needs a backstop afterwards.
		c.drainMu.Lock()
		delete(c.drainingPools, poolID)
		c.drainMu.Unlock()
	}

	tracked := c.reconciler.TrackedPoolRunners(poolID)
	busyLeft := 0
	for _, r := range tracked {
		if r.State != "running" {
			continue
		}
		if graceful && r.IsBusy {
			// Busy runners finish their current job (docs/25 §4.2). No
			// deregistration: the supervisor cannot use a deleted pool's
			// credentials, and the ephemeral runner self-deregisters at exit
			// (docs/25 §4.6).
			busyLeft++
			continue
		}
		c.deregisterRunner(ctx, r)
		_ = c.engine.TerminateRunner(ctx, r.ID)
		c.reconciler.UntrackRunner(poolID, r.ID)
	}

	// Queue purge and diagnostics removal happen in both modes: the pool row
	// is gone either way (docs/25 §4.2).
	c.mu.Lock()
	var remaining []ProvisionRequest
	for _, req := range c.queue {
		if req.PoolID != poolID {
			remaining = append(remaining, req)
		}
	}
	c.queue = remaining
	c.mu.Unlock()

	c.removePoolDiagnostics(poolID)

	if !graceful {
		c.logger.Info("pool drained (hard)", "pool", poolName, "pool_id", poolID)
		return
	}

	if busyLeft == 0 {
		c.logger.Info("pool drained gracefully, no busy runners remained", "pool", poolName, "pool_id", poolID)
		return
	}

	limit := lifetime
	if limit <= 0 {
		limit = DefaultDrainBackstop
	}
	c.drainMu.Lock()
	c.drainingPools[poolID] = drainEntry{name: poolName, limit: limit}
	c.drainMu.Unlock()
	c.logger.Info("pool draining gracefully", "pool", poolName, "pool_id", poolID,
		"busy_left", busyLeft, "lifetime_backstop", limit)
}

// RecycleIdleRunners deregisters and terminates all non-busy tracked runners
// of poolName so the next reconcile respawns them with the pool's current
// configuration (docs/22 §6.2). Busy runners are never touched; failed
// terminations stay tracked for the next audit cycle to reap. Unlike drainPool
// it keeps pool diagnostics and queued provisioning requests — the pool
// continues to exist.
func (c *PoolController) RecycleIdleRunners(ctx context.Context, poolID int64) error {
	if c.reconciler == nil || c.engine == nil {
		return nil
	}

	c.provisionMu.Lock()
	defer c.provisionMu.Unlock()

	for _, r := range c.reconciler.TrackedPoolRunners(poolID) {
		if r.IsBusy || r.State != "running" {
			continue
		}
		c.deregisterRunner(ctx, r)
		if err := c.engine.TerminateRunner(ctx, r.ID); err != nil {
			c.logger.Warn("recycle: failed terminating idle runner", "pool", r.PoolName, "pool_id", poolID, "runner", r.ID, "err", err)
			continue
		}
		c.reconciler.UntrackRunner(poolID, r.ID)
	}
	return nil
}

var _ server.IdleRecycler = (*PoolController)(nil)

// PoolRunners returns all active/idle runners currently tracked for a pool as server.RunnerInstanceInfo.
func (c *PoolController) PoolRunners(poolID int64) []server.RunnerInstanceInfo {
	if c.reconciler == nil {
		return nil
	}
	statuses := c.reconciler.TrackedPoolRunners(poolID)
	res := make([]server.RunnerInstanceInfo, 0, len(statuses))
	for _, s := range statuses {
		res = append(res, server.RunnerInstanceInfo{
			ID:        s.ID,
			Name:      s.Name,
			PoolName:  s.PoolName,
			State:     s.State,
			IPAddress: s.IPAddress,
			SpawnedAt: s.SpawnedAt,
			IsBusy:    s.IsBusy,
		})
	}
	return res
}

// TerminateRunner manually terminates a runner container and reconciles pool tracking state.
func (c *PoolController) TerminateRunner(ctx context.Context, poolID int64, containerID string) error {
	if c.engine != nil {
		if err := c.engine.TerminateRunner(ctx, containerID); err != nil {
			return err
		}
	}
	if c.reconciler != nil {
		c.reconciler.UntrackRunner(poolID, containerID)
	}
	return nil
}
