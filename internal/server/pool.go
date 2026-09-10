package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/noosxe/runnero/internal/cron"
	"github.com/noosxe/runnero/internal/db"
	supervisorv1 "github.com/noosxe/runnero/internal/pb/supervisor/v1"
	"github.com/noosxe/runnero/internal/pb/supervisor/v1/supervisorv1connect"
	"github.com/noosxe/runnero/internal/provider"
)

// PoolDatabase defines the database queries required by PoolService.
// *db.DB satisfies this interface.
type PoolDatabase interface {
	ListRunnerPools(ctx context.Context) ([]db.RunnerPool, error)
	GetRunnerPoolById(ctx context.Context, id int64) (db.RunnerPool, error)
	GetRunnerPoolByName(ctx context.Context, name string) (db.RunnerPool, error)
	CreateRunnerPool(ctx context.Context, arg db.CreateRunnerPoolParams) (db.RunnerPool, error)
	UpdateRunnerPool(ctx context.Context, arg db.UpdateRunnerPoolParams) (db.RunnerPool, error)
	DeleteRunnerPool(ctx context.Context, id int64) error
	CreateAuditLog(ctx context.Context, arg db.CreateAuditLogParams) (db.AuditLog, error)
	GetRenovateConfigByPoolId(ctx context.Context, poolID int64) (db.RenovateConfig, error)
	CreateRenovateConfig(ctx context.Context, arg db.CreateRenovateConfigParams) (db.RenovateConfig, error)
	UpdateRenovateConfig(ctx context.Context, arg db.UpdateRenovateConfigParams) (db.RenovateConfig, error)
	ListPoolTargetsByPoolId(ctx context.Context, poolID int64) ([]db.PoolTarget, error)
	AddPoolTarget(ctx context.Context, arg db.AddPoolTargetParams) (db.PoolTarget, error)
	DeletePoolTargetsByPoolId(ctx context.Context, poolID int64) error
	GetDecryptedAuthProfileById(ctx context.Context, id int64) (*db.DecryptedAuthProfile, error)
}

// PoolDiagnostics encapsulates operational health, intent, and error diagnostics for a runner pool.
type PoolDiagnostics struct {
	HealthStatus        string
	CurrentIntent       string
	LastError           string
	LastErrorCode       string
	LastErrorTimestamp  time.Time
	LastReconciledAt    time.Time
	LastPollAt          time.Time
	LastPollQueuedCount int
	LastPollError       string
}

// PoolStatsProvider provides live active/idle runner counts, diagnostic state, and runtime reload capabilities.
// *orchestrator.PoolController satisfies this interface.
type PoolStatsProvider interface {
	PoolStats(poolID int64) (active int32, idle int32)
	PoolDiagnostics(poolID int64) PoolDiagnostics
	Reload(ctx context.Context) error
}

// IdleRecycler recycles a pool's idle runners so subsequent spawns pick up changed
// pool configuration (docs/22 §6.2). *orchestrator.PoolController satisfies this
// interface; PoolService discovers it by interface assertion on statsProvider so
// absent controllers (unit fakes, web-only operation) degrade to a no-op.
type IdleRecycler interface {
	// RecycleIdleRunners deregisters and terminates all non-busy tracked runners
	// of poolID so the next reconcile respawns them with the pool's current
	// configuration. Busy runners are never touched.
	RecycleIdleRunners(ctx context.Context, poolID int64) error
}

// PoolDrainer tears down a deleted pool's runners after the pool row is gone
// (docs/25 §4.2). *orchestrator.PoolController satisfies this interface;
// PoolService discovers it by interface assertion on statsProvider so absent
// controllers (unit fakes, web-only operation) degrade to a no-op.
type PoolDrainer interface {
	// DrainPool terminates (graceful=false) or gracefully drains (graceful=true)
	// the pool's runners. lifetime is the deleted pool's max_runner_lifetime as a
	// duration (0 = the controller applies DefaultDrainBackstop) used as the
	// graceful backstop for busy runners.
	DrainPool(ctx context.Context, poolID int64, poolName string, lifetime time.Duration, graceful bool)
}

// RunnerInstanceInfo represents an active runner container's runtime state.
type RunnerInstanceInfo struct {
	ID        string
	Name      string
	PoolName  string
	State     string
	IPAddress string
	SpawnedAt time.Time
	IsBusy    bool
}

// RunnerManager provides live runner container inspection and manual kill operations.
// *orchestrator.PoolController satisfies this interface.
type RunnerManager interface {
	PoolRunners(poolID int64) []RunnerInstanceInfo
	TerminateRunner(ctx context.Context, poolID int64, containerID string) error
}

// TargetDiscovererFunc queries available repositories or organizations using a decrypted profile.
// DiscoveryResult encapsulates discovered targets along with optional app installation metadata.
type DiscoveryResult struct {
	Targets       []provider.DiscoveredTarget
	InstallURL    string
	Installations []provider.AppInstallation
}

type TargetDiscovererFunc func(ctx context.Context, profile db.DecryptedAuthProfile, scope string) (*DiscoveryResult, error)

// PoolServiceOption configures a PoolService instance.
type PoolServiceOption func(*PoolService)

// WithDiscoverer overrides the default target discovery function.
func WithDiscoverer(fn TargetDiscovererFunc) PoolServiceOption {
	return func(s *PoolService) {
		s.discoverer = fn
	}
}

// PoolService implements supervisorv1connect.PoolServiceHandler.
type PoolService struct {
	supervisorv1connect.UnimplementedPoolServiceHandler
	db            PoolDatabase
	statsProvider PoolStatsProvider
	runnerMgr     RunnerManager
	discoverer    TargetDiscovererFunc
	logger        *slog.Logger
}

// WithPoolLogger sets the logger for pool service operations.
func WithPoolLogger(logger *slog.Logger) PoolServiceOption {
	return func(s *PoolService) {
		s.logger = logger
	}
}

// NewPoolService constructs a PoolService instance.
func NewPoolService(database PoolDatabase, statsProvider PoolStatsProvider, runnerMgr RunnerManager, opts ...PoolServiceOption) *PoolService {
	s := &PoolService{
		db:            database,
		statsProvider: statsProvider,
		runnerMgr:     runnerMgr,
		logger:        slog.Default(),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// defaultPollIntervalSeconds is the demand-poll cadence applied when a client
// omits poll_interval_seconds (proto zero) on create (docs/24 §5.4/§5.8;
// matches the migration DEFAULT).
const defaultPollIntervalSeconds = 30

// defaultPollInterval maps a zero (omitted) interval to the default cadence.
func defaultPollInterval(seconds int32) int64 {
	if seconds <= 0 {
		return defaultPollIntervalSeconds
	}
	return int64(seconds)
}

// defaultPollIntervalOr maps a zero (omitted) interval to the pool's stored
// cadence, falling back to the default when the stored value is unset.
func defaultPollIntervalOr(seconds int32, stored int64) int64 {
	if seconds > 0 {
		return int64(seconds)
	}
	if stored > 0 {
		return stored
	}
	return defaultPollIntervalSeconds
}

func parseLabels(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return []string{}
	}
	parts := strings.Split(raw, ",")
	res := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			res = append(res, trimmed)
		}
	}
	return res
}

// normalizeValueSet trims, drops empties, de-duplicates, and sorts a set of
// string values (targets or labels) so change detection is order-insensitive
// (docs/22 §6.1).
func normalizeValueSet(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	slices.Sort(out)
	return out
}

// normalizeTargetSet derives the effective target set from a pool payload:
// explicit target URLs when present, otherwise the repository URL (mirrors the
// CreatePool/UpdatePool persistence fallback).
func normalizeTargetSet(p *supervisorv1.Pool) []string {
	targets := p.TargetUrls
	if len(targets) == 0 && strings.TrimSpace(p.RepositoryUrl) != "" {
		targets = []string{p.RepositoryUrl}
	}
	return normalizeValueSet(targets)
}

// normalizeStoredTargets converts persisted pool_targets rows into a
// normalized target set.
func normalizeStoredTargets(targets []db.PoolTarget) []string {
	values := make([]string, 0, len(targets))
	for _, t := range targets {
		values = append(values, t.TargetUrl)
	}
	return normalizeValueSet(values)
}

// normalizeLabels normalizes a stored labels string into a comparable set.
func normalizeLabels(raw string) []string {
	return normalizeValueSet(strings.Split(raw, ","))
}

// spawnIdentityChanged reports whether any spawn-identity field (docs/22 §5.2)
// differs between the persisted pool and the update about to be written.
// Control-plane fields (name, warm-pool sizing, lifetime caps, renovate) are
// excluded: they converge through reconcile without touching runners.
func spawnIdentityChanged(existing db.RunnerPool, existingTargets []string, params db.UpdateRunnerPoolParams, newTargets []string) bool {
	if existing.AuthProfileID != params.AuthProfileID {
		return true
	}
	if existing.RepositoryUrl != params.RepositoryUrl {
		return true
	}
	if existing.Scope != params.Scope {
		return true
	}
	if !slices.Equal(normalizeLabels(existing.Labels), normalizeLabels(params.Labels)) {
		return true
	}
	if existing.RunnerImage != params.RunnerImage {
		return true
	}
	if existing.AllowDocker != params.AllowDocker {
		return true
	}
	if existing.CpuLimit != params.CpuLimit || existing.MemoryLimit != params.MemoryLimit {
		return true
	}
	return !slices.Equal(existingTargets, newTargets)
}

// poolConfigChanges builds before/after audit metadata restricted to changed
// fields (docs/22 §6.1).
func poolConfigChanges(existing db.RunnerPool, existingTargets []string, updated db.RunnerPool, newTargets []string, req *supervisorv1.Pool, renovateBefore db.RenovateConfig, renovateBeforeErr error) map[string]any {
	changes := map[string]any{}
	add := func(name string, before, after any, differs bool) {
		if differs {
			changes[name] = map[string]any{"before": before, "after": after}
		}
	}
	add("name", existing.Name, updated.Name, existing.Name != updated.Name)
	add("auth_profile_id", existing.AuthProfileID, updated.AuthProfileID, existing.AuthProfileID != updated.AuthProfileID)
	add("repository_url", existing.RepositoryUrl, updated.RepositoryUrl, existing.RepositoryUrl != updated.RepositoryUrl)
	add("scope", existing.Scope, updated.Scope, existing.Scope != updated.Scope)
	add("labels", parseLabels(existing.Labels), parseLabels(updated.Labels), !slices.Equal(normalizeLabels(existing.Labels), normalizeLabels(updated.Labels)))
	add("targets", existingTargets, newTargets, !slices.Equal(existingTargets, newTargets))
	add("runner_image", existing.RunnerImage, updated.RunnerImage, existing.RunnerImage != updated.RunnerImage)
	add("allow_docker", existing.AllowDocker, updated.AllowDocker, existing.AllowDocker != updated.AllowDocker)
	add("min_idle_runners", existing.MinIdleRunners, updated.MinIdleRunners, existing.MinIdleRunners != updated.MinIdleRunners)
	add("max_concurrency", existing.MaxConcurrency, updated.MaxConcurrency, existing.MaxConcurrency != updated.MaxConcurrency)
	add("max_runner_lifetime_seconds", existing.MaxRunnerLifetimeSeconds, updated.MaxRunnerLifetimeSeconds, existing.MaxRunnerLifetimeSeconds != updated.MaxRunnerLifetimeSeconds)
	add("poll_fallback", existing.PollFallback, updated.PollFallback, existing.PollFallback != updated.PollFallback)
	add("poll_interval_seconds", existing.PollIntervalSeconds, updated.PollIntervalSeconds, existing.PollIntervalSeconds != updated.PollIntervalSeconds)
	add("cpu_limit", existing.CpuLimit.String, updated.CpuLimit.String, existing.CpuLimit != updated.CpuLimit)
	add("memory_limit", existing.MemoryLimit.String, updated.MemoryLimit.String, existing.MemoryLimit != updated.MemoryLimit)
	if req.Renovate != nil && renovateBeforeErr == nil {
		afterCron := strings.TrimSpace(req.Renovate.CronSchedule)
		afterImage := strings.TrimSpace(req.Renovate.Image)
		if afterImage == "" {
			afterImage = "renovate/renovate:latest"
		}
		add("renovate.enabled", renovateBefore.Enabled, req.Renovate.Enabled, renovateBefore.Enabled != req.Renovate.Enabled)
		add("renovate.cron_schedule", renovateBefore.CronSchedule.String, afterCron, renovateBefore.CronSchedule.String != afterCron)
		add("renovate.image", renovateBefore.Image, afterImage, renovateBefore.Image != afterImage)
	}
	return changes
}

func (s *PoolService) toProto(ctx context.Context, p db.RunnerPool) *supervisorv1.Pool {
	proto := ConvertDBPoolToProto(p, s.statsProvider)
	if s.db != nil {
		if cfg, err := s.db.GetRenovateConfigByPoolId(ctx, p.ID); err == nil {
			proto.Renovate = &supervisorv1.RenovateConfig{
				Enabled:      cfg.Enabled,
				CronSchedule: cfg.CronSchedule.String,
				Image:        cfg.Image,
			}
		}
		enrichPoolTargets(ctx, s.db, p, proto)
	}
	return proto
}

func mapHealthStatusToProto(s string) supervisorv1.PoolHealthStatus {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "healthy":
		return supervisorv1.PoolHealthStatus_POOL_HEALTH_STATUS_HEALTHY
	case "provisioning":
		return supervisorv1.PoolHealthStatus_POOL_HEALTH_STATUS_PROVISIONING
	case "degraded":
		return supervisorv1.PoolHealthStatus_POOL_HEALTH_STATUS_DEGRADED
	case "paused":
		return supervisorv1.PoolHealthStatus_POOL_HEALTH_STATUS_PAUSED
	default:
		return supervisorv1.PoolHealthStatus_POOL_HEALTH_STATUS_HEALTHY
	}
}

// poolTargetsReader is the minimal read access needed to serialize a pool's
// target list.
type poolTargetsReader interface {
	ListPoolTargetsByPoolId(ctx context.Context, poolID int64) ([]db.PoolTarget, error)
}

// enrichPoolTargets populates proto.TargetUrls from the pool_targets table,
// falling back to the legacy single-target repository_url column for records
// predating multi-target support. Every handler that serializes pools must run
// them through this: web clients merge several streams (WatchPools,
// WatchDashboard) into one query cache, so pools serialized with diverging
// target data make UI elements derived from it (e.g. the target count badge)
// flip-flop on every stream tick.
func enrichPoolTargets(ctx context.Context, q poolTargetsReader, p db.RunnerPool, proto *supervisorv1.Pool) {
	if targets, err := q.ListPoolTargetsByPoolId(ctx, p.ID); err == nil && len(targets) > 0 {
		urls := make([]string, 0, len(targets))
		for _, t := range targets {
			urls = append(urls, t.TargetUrl)
		}
		proto.TargetUrls = urls
	} else if p.RepositoryUrl != "" {
		proto.TargetUrls = []string{p.RepositoryUrl}
	}
}

// ConvertDBPoolToProto converts a db.RunnerPool row into a supervisorv1.Pool protobuf message,
// attaching runtime active/idle runner counts and operational diagnostics from the provided stats provider if available.
func ConvertDBPoolToProto(p db.RunnerPool, stats PoolStatsProvider) *supervisorv1.Pool {
	protoPool := &supervisorv1.Pool{
		Id:                       p.ID,
		Name:                     p.Name,
		Provider:                 p.Provider,
		RepositoryUrl:            p.RepositoryUrl,
		MinIdleRunners:           int32(p.MinIdleRunners),
		MaxConcurrency:           int32(p.MaxConcurrency),
		Labels:                   parseLabels(p.Labels),
		RunnerImage:              p.RunnerImage,
		AllowDocker:              p.AllowDocker,
		AuthProfileId:            p.AuthProfileID,
		Scope:                    p.Scope,
		CpuLimit:                 p.CpuLimit.String,
		MemoryLimit:              p.MemoryLimit.String,
		MaxRunnerLifetimeSeconds: int32(p.MaxRunnerLifetimeSeconds),
		PollFallback:             p.PollFallback,
		PollIntervalSeconds:      int32(p.PollIntervalSeconds),
	}

	if p.RepositoryUrl != "" {
		protoPool.TargetUrls = []string{p.RepositoryUrl}
	}

	if stats != nil {
		active, idle := stats.PoolStats(p.ID)
		protoPool.ActiveRunners = active
		protoPool.IdleRunners = idle

		diag := stats.PoolDiagnostics(p.ID)
		protoPool.HealthStatus = mapHealthStatusToProto(diag.HealthStatus)
		protoPool.CurrentIntent = diag.CurrentIntent
		protoPool.LastError = diag.LastError
		protoPool.LastErrorCode = diag.LastErrorCode
		if !diag.LastErrorTimestamp.IsZero() {
			protoPool.LastErrorTimestamp = diag.LastErrorTimestamp.Format(time.RFC3339)
		}
		if !diag.LastReconciledAt.IsZero() {
			protoPool.LastReconciledAt = diag.LastReconciledAt.Format(time.RFC3339)
		}
		if !diag.LastPollAt.IsZero() {
			protoPool.LastPollAt = diag.LastPollAt.Format(time.RFC3339)
		}
		protoPool.LastPollQueuedCount = int32(diag.LastPollQueuedCount)
		protoPool.LastPollError = diag.LastPollError
	}

	return protoPool
}

func validatePoolInput(p *supervisorv1.Pool) error {
	if p == nil {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("pool payload is required"))
	}
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("pool name must not be empty"))
	}

	provider := strings.ToLower(strings.TrimSpace(p.Provider))
	switch provider {
	case "github", "gitea", "forgejo":
	default:
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unsupported provider %q; must be 'github', 'gitea', or 'forgejo'", p.Provider))
	}

	if strings.TrimSpace(p.RepositoryUrl) == "" {
		if len(p.TargetUrls) > 0 {
			p.RepositoryUrl = strings.TrimSpace(p.TargetUrls[0])
		} else {
			return connect.NewError(connect.CodeInvalidArgument, errors.New("repository_url or target_urls must not be empty"))
		}
	}

	scope := strings.ToLower(strings.TrimSpace(p.Scope))
	if scope == "" {
		scope = "repo"
	}
	if scope != "repo" && scope != "org" && scope != "global" {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid pool scope %q: must be 'repo' or 'org'", p.Scope))
	}

	targets := p.TargetUrls
	if len(targets) == 0 && p.RepositoryUrl != "" {
		targets = []string{p.RepositoryUrl}
	}

	for _, target := range targets {
		t := strings.TrimSpace(target)
		if t == "" {
			continue
		}
		u, err := url.Parse(t)
		if err != nil || u.Host == "" {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid target url %q", target))
		}
		trimmedPath := strings.Trim(u.Path, "/")
		parts := strings.Split(trimmedPath, "/")
		if scope == "repo" && (trimmedPath == "" || len(parts) < 2) {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("target %q is not a repository URL (pool scope is 'repo'); mixing repositories and organizations is not allowed", target))
		}
		if scope == "org" && len(parts) >= 2 {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("target %q is a repository URL (pool scope is 'org'); mixing repositories and organizations is not allowed", target))
		}
	}

	// Gitea and Forgejo require allow_docker=true (docs/05 §4)
	if (provider == "gitea" || provider == "forgejo") && !p.AllowDocker {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("gitea and forgejo pools require allow_docker=true (docs/05 §4)"))
	}

	if p.AuthProfileId <= 0 {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("auth_profile_id must be a valid positive identifier"))
	}

	if p.MinIdleRunners < 0 {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("min_idle_runners must be non-negative"))
	}
	if p.MaxConcurrency < 0 {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("max_concurrency must be non-negative"))
	}
	if p.MaxRunnerLifetimeSeconds < 0 {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("max_runner_lifetime_seconds must be non-negative"))
	}

	// Demand polling validation (docs/24 §5.7): Gitea has no repo-scoped
	// queued-jobs API (docs/24 §4); Forgejo polls natively regardless of the flag.
	if p.PollFallback && provider == "gitea" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("poll_fallback is not supported for gitea pools (no repo-scoped queued-jobs API)"))
	}
	if p.PollIntervalSeconds != 0 && (p.PollIntervalSeconds < 15 || p.PollIntervalSeconds > 3600) {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("poll_interval_seconds must be between 15 and 3600"))
	}

	if p.Renovate != nil && p.Renovate.Enabled && strings.TrimSpace(p.Renovate.CronSchedule) != "" {
		if _, err := cron.ParseSchedule(strings.TrimSpace(p.Renovate.CronSchedule)); err != nil {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid renovate cron schedule: %w", err))
		}
	}

	return nil
}

// ListPools retrieves all runner pools with live active/idle stats.
func (s *PoolService) ListPools(ctx context.Context, _ *connect.Request[supervisorv1.ListPoolsRequest]) (*connect.Response[supervisorv1.ListPoolsResponse], error) {
	pools, err := s.db.ListRunnerPools(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("listing runner pools: %w", err))
	}

	resp := &supervisorv1.ListPoolsResponse{
		Pools: make([]*supervisorv1.Pool, 0, len(pools)),
	}
	for _, p := range pools {
		resp.Pools = append(resp.Pools, s.toProto(ctx, p))
	}

	return connect.NewResponse(resp), nil
}

// CreatePool persists a new runner pool, creates an audit log, and notifies the controller loop.
func (s *PoolService) CreatePool(ctx context.Context, req *connect.Request[supervisorv1.CreatePoolRequest]) (*connect.Response[supervisorv1.CreatePoolResponse], error) {
	pool := req.Msg.Pool
	if err := validatePoolInput(pool); err != nil {
		return nil, err
	}

	scope := strings.TrimSpace(pool.Scope)
	if scope == "" {
		scope = "repo"
	}

	labelsStr := strings.Join(pool.Labels, ",")

	created, err := s.db.CreateRunnerPool(ctx, db.CreateRunnerPoolParams{
		Name:                     strings.TrimSpace(pool.Name),
		Provider:                 strings.ToLower(strings.TrimSpace(pool.Provider)),
		RepositoryUrl:            strings.TrimSpace(pool.RepositoryUrl),
		Scope:                    scope,
		AuthProfileID:            pool.AuthProfileId,
		MinIdleRunners:           int64(pool.MinIdleRunners),
		MaxConcurrency:           int64(pool.MaxConcurrency),
		Labels:                   labelsStr,
		RunnerImage:              strings.TrimSpace(pool.RunnerImage),
		AllowDocker:              pool.AllowDocker,
		MaxRunnerLifetimeSeconds: int64(pool.MaxRunnerLifetimeSeconds),
		CpuLimit:                 sql.NullString{String: pool.CpuLimit, Valid: pool.CpuLimit != ""},
		MemoryLimit:              sql.NullString{String: pool.MemoryLimit, Valid: pool.MemoryLimit != ""},
		PollFallback:             pool.PollFallback,
		PollIntervalSeconds:      defaultPollInterval(pool.PollIntervalSeconds),
	})
	if err != nil {
		if strings.Contains(err.Error(), "FOREIGN KEY") {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("auth_profile_id %d does not exist", pool.AuthProfileId))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("creating runner pool: %w", err))
	}

	if pool.Renovate != nil {
		if pool.Renovate.Enabled && strings.TrimSpace(pool.Renovate.CronSchedule) != "" {
			if _, err := cron.ParseSchedule(strings.TrimSpace(pool.Renovate.CronSchedule)); err != nil {
				return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid renovate cron schedule: %w", err))
			}
		}
		img := strings.TrimSpace(pool.Renovate.Image)
		if img == "" {
			img = "renovate/renovate:latest"
		}
		cronSched := strings.TrimSpace(pool.Renovate.CronSchedule)
		_, _ = s.db.CreateRenovateConfig(ctx, db.CreateRenovateConfigParams{
			PoolID:       created.ID,
			Enabled:      pool.Renovate.Enabled,
			CronSchedule: sql.NullString{String: cronSched, Valid: cronSched != ""},
			Image:        img,
		})
	}

	// Persist target URLs into pool_targets
	targetURLs := pool.TargetUrls
	if len(targetURLs) == 0 && created.RepositoryUrl != "" {
		targetURLs = []string{created.RepositoryUrl}
	}
	for _, t := range targetURLs {
		t = strings.TrimSpace(t)
		if t != "" {
			_, _ = s.db.AddPoolTarget(ctx, db.AddPoolTargetParams{
				PoolID:    created.ID,
				TargetUrl: t,
			})
		}
	}

	recordAuditLog(ctx, s.db, "pool.create", "runner_pool", &created.ID, map[string]any{
		"name":            created.Name,
		"provider":        created.Provider,
		"repository_url":  created.RepositoryUrl,
		"scope":           created.Scope,
		"min_idle":        created.MinIdleRunners,
		"max_concurrency": created.MaxConcurrency,
	})

	if s.statsProvider != nil {
		_ = s.statsProvider.Reload(ctx)
	}

	return connect.NewResponse(&supervisorv1.CreatePoolResponse{
		Pool: s.toProto(ctx, created),
	}), nil
}

// UpdatePool updates an existing runner pool per docs/22 §5.5: validate, load the
// existing row, guard renames on busy runners, recycle idle runners when spawn
// identity changes, persist with mapped error codes, audit before/after, and
// notify the controller loop.
func (s *PoolService) UpdatePool(ctx context.Context, req *connect.Request[supervisorv1.UpdatePoolRequest]) (*connect.Response[supervisorv1.UpdatePoolResponse], error) {
	pool := req.Msg.Pool
	if err := validatePoolInput(pool); err != nil {
		return nil, err
	}
	if pool.Id <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("pool id must be specified for update"))
	}

	existing, err := s.db.GetRunnerPoolById(ctx, pool.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("pool id %d not found: %w", pool.Id, err))
	}

	provider := strings.ToLower(strings.TrimSpace(pool.Provider))
	if provider != existing.Provider {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"provider is immutable: pool %d was created as %q; recreate the pool to change provider", pool.Id, existing.Provider))
	}

	scope := strings.TrimSpace(pool.Scope)
	if scope == "" {
		scope = existing.Scope
	}

	labelsStr := strings.Join(pool.Labels, ",")

	params := db.UpdateRunnerPoolParams{
		ID:                       pool.Id,
		Name:                     strings.TrimSpace(pool.Name),
		Provider:                 provider,
		RepositoryUrl:            strings.TrimSpace(pool.RepositoryUrl),
		Scope:                    scope,
		AuthProfileID:            pool.AuthProfileId,
		MinIdleRunners:           int64(pool.MinIdleRunners),
		MaxConcurrency:           int64(pool.MaxConcurrency),
		Labels:                   labelsStr,
		RunnerImage:              strings.TrimSpace(pool.RunnerImage),
		AllowDocker:              pool.AllowDocker,
		MaxRunnerLifetimeSeconds: int64(pool.MaxRunnerLifetimeSeconds),
		CpuLimit:                 sql.NullString{String: pool.CpuLimit, Valid: pool.CpuLimit != ""},
		MemoryLimit:              sql.NullString{String: pool.MemoryLimit, Valid: pool.MemoryLimit != ""},
		PollFallback:             pool.PollFallback,
		// The interval is a DB-level knob with no UI field (docs/24 §5.4): a client
		// that omits it (proto zero) preserves the stored cadence.
		PollIntervalSeconds: defaultPollIntervalOr(pool.PollIntervalSeconds, existing.PollIntervalSeconds),
	}

	newTargets := normalizeTargetSet(pool)
	storedTargets, err := s.db.ListPoolTargetsByPoolId(ctx, pool.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("loading pool targets: %w", err))
	}
	existingTargets := normalizeStoredTargets(storedTargets)

	identityChanged := spawnIdentityChanged(existing, existingTargets, params, newTargets)

	// Renames are metadata-only: the controller tracks runners by pool id, so a
	// rename neither orphans nor recycles live runners — spawned containers keep
	// their spawn-time pool-name label until they recycle naturally (RUN-126,
	// docs/22 §5.4). Spawn-identity edits recycle idle runners so respawns pick
	// up the new configuration; busy runners are never touched (docs/22 §5.2).
	if identityChanged {
		s.recycleIdleRunners(ctx, existing.ID)
	}

	updated, err := s.db.UpdateRunnerPool(ctx, params)
	if err != nil {
		if db.IsUniqueConstraintError(err) {
			return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("pool name %q already exists", params.Name))
		}
		if strings.Contains(err.Error(), "FOREIGN KEY") {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("auth_profile_id %d does not exist", pool.AuthProfileId))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("updating runner pool: %w", err))
	}

	renovateBefore, renovateBeforeErr := s.db.GetRenovateConfigByPoolId(ctx, pool.Id)

	if pool.Renovate != nil {
		img := strings.TrimSpace(pool.Renovate.Image)
		if img == "" {
			img = "renovate/renovate:latest"
		}
		cronSched := strings.TrimSpace(pool.Renovate.CronSchedule)
		_, err := s.db.UpdateRenovateConfig(ctx, db.UpdateRenovateConfigParams{
			PoolID:       updated.ID,
			Enabled:      pool.Renovate.Enabled,
			CronSchedule: sql.NullString{String: cronSched, Valid: cronSched != ""},
			Image:        img,
		})
		switch {
		case err == nil:
		case errors.Is(err, sql.ErrNoRows):
			if _, cerr := s.db.CreateRenovateConfig(ctx, db.CreateRenovateConfigParams{
				PoolID:       updated.ID,
				Enabled:      pool.Renovate.Enabled,
				CronSchedule: sql.NullString{String: cronSched, Valid: cronSched != ""},
				Image:        img,
			}); cerr != nil {
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("creating renovate config: %w", cerr))
			}
		default:
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("updating renovate config: %w", err))
		}
	}

	// Rewrite pool_targets only when the normalized target set changed, so
	// full-pool round-trips (e.g. the Renovate tab) do not churn target rows
	// (docs/22 §6.1).
	if !slices.Equal(existingTargets, newTargets) {
		if err := s.db.DeletePoolTargetsByPoolId(ctx, pool.Id); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("replacing pool targets: %w", err))
		}
		for _, t := range newTargets {
			if _, err := s.db.AddPoolTarget(ctx, db.AddPoolTargetParams{
				PoolID:    pool.Id,
				TargetUrl: t,
			}); err != nil {
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("adding pool target %q: %w", t, err))
			}
		}
	}

	recordAuditLog(ctx, s.db, "pool.update", "runner_pool", &updated.ID, map[string]any{
		"name":     updated.Name,
		"provider": updated.Provider,
		"changes":  poolConfigChanges(existing, existingTargets, updated, newTargets, pool, renovateBefore, renovateBeforeErr),
	})

	if s.statsProvider != nil {
		_ = s.statsProvider.Reload(ctx)
	}

	return connect.NewResponse(&supervisorv1.UpdatePoolResponse{
		Pool: s.toProto(ctx, updated),
	}), nil
}

// recycleIdleRunners asks the controller (when present) to recycle the pool's
// idle runners so respawns pick up the updated configuration. Best-effort:
// failures are logged and the update proceeds — recycled runners respawn on
// the next reconcile tick either way (docs/22 §5.5).
func (s *PoolService) recycleIdleRunners(ctx context.Context, poolID int64) {
	recycler, ok := s.statsProvider.(IdleRecycler)
	if !ok || recycler == nil {
		return
	}
	if err := recycler.RecycleIdleRunners(ctx, poolID); err != nil {
		s.logger.Warn("failed recycling idle runners after pool update", "pool_id", poolID, "err", err)
	}
}

// DeletePool removes a runner pool from the database, emits an audit log, and notifies the controller.
func (s *PoolService) DeletePool(ctx context.Context, req *connect.Request[supervisorv1.DeletePoolRequest]) (*connect.Response[supervisorv1.DeletePoolResponse], error) {
	if req.Msg.Id <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid pool id"))
	}

	existing, err := s.db.GetRunnerPoolById(ctx, req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("pool id %d not found: %w", req.Msg.Id, err))
	}

	_ = s.db.DeletePoolTargetsByPoolId(ctx, req.Msg.Id)
	if err := s.db.DeleteRunnerPool(ctx, req.Msg.Id); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("deleting runner pool: %w", err))
	}

	recordAuditLog(ctx, s.db, "pool.delete", "runner_pool", &existing.ID, map[string]any{
		"name":           existing.Name,
		"provider":       existing.Provider,
		"drain_graceful": req.Msg.DrainGraceful,
	})

	// Tear the runners down explicitly, before the Reload-triggered reconcile
	// reaches the removed-pool fallback: the RPC path knows the pool's lifetime
	// switch and drain mode, the fallback does not (docs/25 §4.2, §4.4).
	if drainer, ok := s.statsProvider.(PoolDrainer); ok && drainer != nil {
		drainer.DrainPool(ctx, existing.ID, existing.Name,
			time.Duration(existing.MaxRunnerLifetimeSeconds)*time.Second, req.Msg.DrainGraceful)
	}
	if s.statsProvider != nil {
		_ = s.statsProvider.Reload(ctx)
	}

	return connect.NewResponse(&supervisorv1.DeletePoolResponse{
		Success: true,
	}), nil
}

// WatchPools provides near-realtime server-streaming push of pool states and runner counts.
func (s *PoolService) WatchPools(ctx context.Context, req *connect.Request[supervisorv1.WatchPoolsRequest], stream *connect.ServerStream[supervisorv1.WatchPoolsResponse]) error {
	intervalMs := req.Msg.IntervalMs
	if intervalMs < 250 {
		intervalMs = 1000
	}
	if intervalMs > 10000 {
		intervalMs = 10000
	}
	ticker := time.NewTicker(time.Duration(intervalMs) * time.Millisecond)
	defer ticker.Stop()

	sendSnapshot := func() error {
		dbPools, err := s.db.ListRunnerPools(ctx)
		if err != nil {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("listing runner pools: %w", err))
		}
		protoPools := make([]*supervisorv1.Pool, 0, len(dbPools))
		for _, p := range dbPools {
			protoPools = append(protoPools, s.toProto(ctx, p))
		}
		return stream.Send(&supervisorv1.WatchPoolsResponse{
			Pools: protoPools,
		})
	}

	// Send initial snapshot immediately
	if err := sendSnapshot(); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := sendSnapshot(); err != nil {
				return err
			}
		}
	}
}

// ListRunners returns the active container instances for a specified pool.
func (s *PoolService) ListRunners(ctx context.Context, req *connect.Request[supervisorv1.ListRunnersRequest]) (*connect.Response[supervisorv1.ListRunnersResponse], error) {
	if req.Msg.PoolId <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("pool_id must be greater than 0"))
	}
	p, err := s.db.GetRunnerPoolById(ctx, req.Msg.PoolId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("pool id %d not found: %w", req.Msg.PoolId, err))
	}

	runners := s.getRunnerInstances(p)
	return connect.NewResponse(&supervisorv1.ListRunnersResponse{
		Runners: runners,
	}), nil
}

// TerminateRunner manually terminates an active runner container instance.
func (s *PoolService) TerminateRunner(ctx context.Context, req *connect.Request[supervisorv1.TerminateRunnerRequest]) (*connect.Response[supervisorv1.TerminateRunnerResponse], error) {
	if req.Msg.PoolId <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("pool_id must be greater than 0"))
	}
	containerID := strings.TrimSpace(req.Msg.ContainerId)
	if containerID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("container_id must not be empty"))
	}

	p, err := s.db.GetRunnerPoolById(ctx, req.Msg.PoolId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("pool id %d not found: %w", req.Msg.PoolId, err))
	}

	if s.runnerMgr != nil {
		if err := s.runnerMgr.TerminateRunner(ctx, p.ID, containerID); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("terminating runner %q: %w", containerID, err))
		}
	}

	recordAuditLog(ctx, s.db, "runner.terminate", "runner_container", &p.ID, map[string]any{
		"container_id": containerID,
		"pool_name":    p.Name,
		"pool_id":      p.ID,
	})

	if s.statsProvider != nil {
		_ = s.statsProvider.Reload(ctx)
	}

	return connect.NewResponse(&supervisorv1.TerminateRunnerResponse{
		Success: true,
	}), nil
}

// WatchRunners provides near-realtime server-streaming push of active runner instances for a pool.
func (s *PoolService) WatchRunners(ctx context.Context, req *connect.Request[supervisorv1.WatchRunnersRequest], stream *connect.ServerStream[supervisorv1.WatchRunnersResponse]) error {
	if req.Msg.PoolId <= 0 {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("pool_id must be greater than 0"))
	}
	p, err := s.db.GetRunnerPoolById(ctx, req.Msg.PoolId)
	if err != nil {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("pool id %d not found: %w", req.Msg.PoolId, err))
	}

	intervalMs := req.Msg.IntervalMs
	if intervalMs < 250 {
		intervalMs = 1000
	}
	if intervalMs > 10000 {
		intervalMs = 10000
	}
	ticker := time.NewTicker(time.Duration(intervalMs) * time.Millisecond)
	defer ticker.Stop()

	sendSnapshot := func() error {
		runners := s.getRunnerInstances(p)
		var active, idle int32
		health := supervisorv1.PoolHealthStatus_POOL_HEALTH_STATUS_HEALTHY
		var intent, lastErr, lastErrCode, lastErrTime, lastReconciled string

		if s.statsProvider != nil {
			active, idle = s.statsProvider.PoolStats(p.ID)
			diag := s.statsProvider.PoolDiagnostics(p.ID)
			health = mapHealthStatusToProto(diag.HealthStatus)
			intent = diag.CurrentIntent
			lastErr = diag.LastError
			lastErrCode = diag.LastErrorCode
			if !diag.LastErrorTimestamp.IsZero() {
				lastErrTime = diag.LastErrorTimestamp.Format(time.RFC3339)
			}
			if !diag.LastReconciledAt.IsZero() {
				lastReconciled = diag.LastReconciledAt.Format(time.RFC3339)
			}
		}

		return stream.Send(&supervisorv1.WatchRunnersResponse{
			Runners:            runners,
			ActiveRunners:      active,
			IdleRunners:        idle,
			HealthStatus:       health,
			CurrentIntent:      intent,
			LastError:          lastErr,
			LastErrorCode:      lastErrCode,
			LastErrorTimestamp: lastErrTime,
			LastReconciledAt:   lastReconciled,
		})
	}

	if err := sendSnapshot(); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := sendSnapshot(); err != nil {
				return err
			}
		}
	}
}

func (s *PoolService) getRunnerInstances(p db.RunnerPool) []*supervisorv1.RunnerInstance {
	if s.runnerMgr == nil {
		return []*supervisorv1.RunnerInstance{}
	}
	rawRunners := s.runnerMgr.PoolRunners(p.ID)
	res := make([]*supervisorv1.RunnerInstance, 0, len(rawRunners))
	for _, r := range rawRunners {
		status := "idle"
		if r.State != "running" {
			status = r.State
		} else if r.IsBusy {
			status = "busy"
		}
		uptime := int64(0)
		if !r.SpawnedAt.IsZero() {
			uptime = int64(time.Since(r.SpawnedAt).Seconds())
			if uptime < 0 {
				uptime = 0
			}
		}
		// PoolName comes from the pool row, not the container's spawn-time
		// label, so it stays correct across renames (RUN-126).
		res = append(res, &supervisorv1.RunnerInstance{
			ContainerId:   r.ID,
			Name:          r.Name,
			PoolName:      p.Name,
			Status:        status,
			IpAddress:     r.IPAddress,
			UptimeSeconds: uptime,
			SpawnedAt:     r.SpawnedAt.Format(time.RFC3339),
			CpuLimit:      p.CpuLimit.String,
			MemoryLimit:   p.MemoryLimit.String,
		})
	}
	return res
}

func defaultDiscover(ctx context.Context, profile db.DecryptedAuthProfile, scope string) (*DiscoveryResult, error) {
	prov, err := provider.DefaultRegistry.Build(ctx, profile)
	if err != nil {
		return nil, fmt.Errorf("building provider client: %w", err)
	}
	var targets []provider.DiscoveredTarget
	if scope == "org" {
		targets, err = prov.DiscoverOrganizations(ctx)
	} else {
		targets, err = prov.DiscoverRepositories(ctx)
	}
	if err != nil {
		return nil, err
	}

	result := &DiscoveryResult{
		Targets: targets,
	}

	if metaProv, ok := prov.(provider.AppMetadataProvider); ok {
		installURL, insts, err := metaProv.GetAppMetadata(ctx)
		if err == nil {
			result.InstallURL = installURL
			result.Installations = insts
		}
	}

	return result, nil
}

// compareDiscoveredName orders discovered-entity names case-insensitively
// ("acme/app" and "Acme/APP" interleave naturally), breaking ties on the raw
// name so the order is a total one (RUN-150).
func compareDiscoveredName(a, b string) int {
	al, bl := strings.ToLower(a), strings.ToLower(b)
	if c := strings.Compare(al, bl); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}

// DiscoverTargets queries accessible repositories or organizations using an auth profile.
func (s *PoolService) DiscoverTargets(ctx context.Context, req *connect.Request[supervisorv1.DiscoverTargetsRequest]) (*connect.Response[supervisorv1.DiscoverTargetsResponse], error) {
	if req.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("request payload is required"))
	}
	if req.Msg.AuthProfileId <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("auth_profile_id is required"))
	}
	scope := strings.ToLower(strings.TrimSpace(req.Msg.Scope))
	if scope == "" {
		scope = "repo"
	}
	if scope != "repo" && scope != "org" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid scope %q: must be 'repo' or 'org'", req.Msg.Scope))
	}

	profile, err := s.db.GetDecryptedAuthProfileById(ctx, req.Msg.AuthProfileId)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("auth profile %d not found", req.Msg.AuthProfileId))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("fetching auth profile %d: %w", req.Msg.AuthProfileId, err))
	}

	discoverFn := s.discoverer
	if discoverFn == nil {
		discoverFn = defaultDiscover
	}

	result, err := discoverFn(ctx, *profile, scope)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("discovering %s targets: %w", scope, err))
	}
	// Stable, provider-independent ordering (RUN-150): the providers return
	// targets in whatever order their listing endpoints use — GitHub App
	// discovery even groups repositories per installation — so sort here.
	// Case-insensitive on the name, with a raw-name tie-break for a total
	// order, so every consumer of this RPC gets a deterministic list.
	slices.SortFunc(result.Targets, func(a, b provider.DiscoveredTarget) int {
		return compareDiscoveredName(a.FullName, b.FullName)
	})
	slices.SortFunc(result.Installations, func(a, b provider.AppInstallation) int {
		return compareDiscoveredName(a.AccountLogin, b.AccountLogin)
	})

	protoTargets := make([]*supervisorv1.DiscoveredTarget, 0, len(result.Targets))
	for _, t := range result.Targets {
		protoTargets = append(protoTargets, &supervisorv1.DiscoveredTarget{
			Name:        t.Name,
			FullName:    t.FullName,
			HtmlUrl:     t.HTMLURL,
			Description: t.Description,
			IsPrivate:   t.IsPrivate,
			AvatarUrl:   t.AvatarURL,
		})
	}

	protoInstallations := make([]*supervisorv1.AppInstallation, 0, len(result.Installations))
	for _, inst := range result.Installations {
		protoInstallations = append(protoInstallations, &supervisorv1.AppInstallation{
			Id:                  inst.ID,
			AccountLogin:        inst.AccountLogin,
			AccountType:         inst.AccountType,
			HtmlUrl:             inst.HTMLURL,
			RepositorySelection: inst.RepositorySelection,
		})
	}

	return connect.NewResponse(&supervisorv1.DiscoverTargetsResponse{
		Targets:       protoTargets,
		InstallUrl:    result.InstallURL,
		Installations: protoInstallations,
	}), nil
}
