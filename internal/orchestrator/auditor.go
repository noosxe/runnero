package orchestrator

import (
	"context"
	"fmt"
	"sync"
	"time"
)

const (
	// DefaultAuditInterval is the baseline periodic reconciler interval (~10s per docs/03 §3).
	DefaultAuditInterval = 10 * time.Second
)

// AuditReport summarizes container status changes detected during an audit reconciliation cycle.
type AuditReport struct {
	TotalTracked int            `json:"total_tracked"`
	Active       []RunnerStatus `json:"active"`
	Exited       []RunnerStatus `json:"exited"`
	Adopted      []RunnerStatus `json:"adopted"`
	Disappeared  []string       `json:"disappeared"`
}

// PoolNameResolver resolves a runner pool's database id from its name. It is
// used to adopt containers spawned before the pool-id label existed (RUN-126):
// their only pool association is the spawn-time name label.
type PoolNameResolver func(name string) (int64, bool)

// Reconciler maintains in-memory runner pool tracking state, reconciling it with
// host container engine state on supervisor boot and across periodic audit cycles (docs/03 §2, §3).
//
// Tracking is keyed by pool database id, which survives renames (docs/22 §5.4,
// RUN-126); the id-keyed zero bucket holds containers whose pool could not be
// resolved (unmanaged leftovers, pools renamed or deleted between spawn and audit).
type Reconciler struct {
	provider ContainerProvider

	// resolvePoolID promotes legacy containers (no pool-id label) by their
	// spawn-time pool name. May be nil, in which case such containers land in
	// the unresolved (zero-id) bucket.
	resolvePoolID PoolNameResolver

	mu sync.RWMutex
	// tracked maps poolID -> map[containerID]RunnerStatus
	tracked map[int64]map[string]RunnerStatus
}

// NewReconciler creates a new runner state reconciler.
func NewReconciler(provider ContainerProvider) *Reconciler {
	return &Reconciler{
		provider: provider,
		tracked:  make(map[int64]map[string]RunnerStatus),
	}
}

// SetPoolNameResolver installs the legacy-adoption name resolver (RUN-126).
// It must be called before the first audit; controllers typically wire it to a
// database lookup closed over their pool repository.
func (r *Reconciler) SetPoolNameResolver(resolver PoolNameResolver) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resolvePoolID = resolver
}

// RebuildState performs boot-time reconciliation. When the supervisor starts or restarts
// mid-flight, it queries all supervisor-managed containers from the host engine, adopting
// them into in-memory tracking to prevent orphan or duplicate spawns (docs/03 §2).
func (r *Reconciler) RebuildState(ctx context.Context) (AuditReport, error) {
	return r.Audit(ctx)
}

// Audit queries the host container provider, synchronizes in-memory pool state,
// and returns a detailed report of active, exited, adopted, and disappeared containers.
func (r *Reconciler) Audit(ctx context.Context) (AuditReport, error) {
	if r.provider == nil {
		return AuditReport{}, fmt.Errorf("container provider is nil")
	}

	liveStatuses, err := r.provider.AuditRunners(ctx)
	if err != nil {
		return AuditReport{}, fmt.Errorf("auditing runners from provider: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	report := AuditReport{}
	liveMap := make(map[string]RunnerStatus, len(liveStatuses))

	for _, s := range liveStatuses {
		liveMap[s.ID] = s

		if s.PoolID == 0 && s.PoolName != "" {
			// Container spawned before the pool-id label existed (RUN-126):
			// promote it by its spawn-time name so boot adoption of in-flight
			// runners survives the upgrade (docs/03 §2).
			if poolID, ok := r.resolvePoolID(s.PoolName); ok {
				s.PoolID = poolID
			}
		}

		poolMap, exists := r.tracked[s.PoolID]
		if !exists {
			poolMap = make(map[string]RunnerStatus)
			r.tracked[s.PoolID] = poolMap
		}

		if existing, alreadyTracked := poolMap[s.ID]; alreadyTracked {
			if !s.IsBusy && existing.IsBusy {
				s.IsBusy = existing.IsBusy
			}
			if s.SpawnedAt.IsZero() && !existing.SpawnedAt.IsZero() {
				s.SpawnedAt = existing.SpawnedAt
			}
			if s.BusySince.IsZero() && !existing.BusySince.IsZero() {
				s.BusySince = existing.BusySince
			}
			if !s.OnDemand && existing.OnDemand {
				s.OnDemand = existing.OnDemand
			}
		} else {
			// Container was discovered on host but not yet in memory -> adopted
			// docs/23 §4.4.1: the host listing cannot report busy state, so every
			// adopted running container gets the conservative spawn anchor. A runner
			// already mid-job at adoption thus keeps exactly the pre-docs/23
			// spawn+lifetime bound; set-once semantics then keep it there for the
			// container's life.
			if s.State == "running" && !s.SpawnedAt.IsZero() {
				s.BusySince = s.SpawnedAt
			}
			report.Adopted = append(report.Adopted, s)
		}

		// Update tracked status with latest live state, keeping the forge-assigned
		// runner id sticky: the host listing cannot report it (only the forge API
		// listing can), and clobbering it would break conclusion enrichment on
		// the death path, which runs before the next listing refresh (docs/21 §5.3).
		if prev, ok := poolMap[s.ID]; ok && s.ForgeID == 0 && prev.ForgeID != 0 {
			s.ForgeID = prev.ForgeID
		}
		poolMap[s.ID] = s

		if s.State == "running" {
			report.Active = append(report.Active, s)
		} else {
			report.Exited = append(report.Exited, s)
		}
	}

	// Detect containers previously tracked that have disappeared from the host engine
	for poolID, poolMap := range r.tracked {
		for id := range poolMap {
			if _, stillPresent := liveMap[id]; !stillPresent {
				report.Disappeared = append(report.Disappeared, id)
				delete(poolMap, id)
			}
		}
		if len(poolMap) == 0 {
			delete(r.tracked, poolID)
		}
	}

	total := 0
	for _, poolMap := range r.tracked {
		total += len(poolMap)
	}
	report.TotalTracked = total

	return report, nil
}

// TrackedPoolRunners returns a snapshot of all currently tracked runners for a pool.
func (r *Reconciler) TrackedPoolRunners(poolID int64) []RunnerStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()

	poolMap, exists := r.tracked[poolID]
	if !exists {
		return nil
	}

	runners := make([]RunnerStatus, 0, len(poolMap))
	for _, s := range poolMap {
		runners = append(runners, s)
	}
	return runners
}

// TrackRunner registers a newly spawned runner into tracking.
func (r *Reconciler) TrackRunner(status RunnerStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()

	poolMap, exists := r.tracked[status.PoolID]
	if !exists {
		poolMap = make(map[string]RunnerStatus)
		r.tracked[status.PoolID] = poolMap
	}
	poolMap[status.ID] = status
}

// UntrackRunner removes a terminated runner from tracking.
func (r *Reconciler) UntrackRunner(poolID int64, containerID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if poolMap, exists := r.tracked[poolID]; exists {
		delete(poolMap, containerID)
		if len(poolMap) == 0 {
			delete(r.tracked, poolID)
		}
	}
}

// MarkRunnerBusy updates the busy status of a runner matching the given name or ID.
//
// It also maintains the runner-lifetime anchor (docs/23 §4.2): the idle→busy
// transition stamps BusySince with the current time, set-once; busy→idle
// busy→idle flips leave it in place (sticky) so a listing flap cannot extend a
// hung job's wall clock. The anchor clears only when the tracked state is
// dropped with the container.
func (r *Reconciler) MarkRunnerBusy(runnerNameOrID string, busy bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, poolMap := range r.tracked {
		for id, status := range poolMap {
			if status.ID == runnerNameOrID || status.Name == runnerNameOrID {
				status.IsBusy = busy
				if busy && status.BusySince.IsZero() {
					status.BusySince = time.Now().UTC()
				}
				poolMap[id] = status
				return
			}
		}
	}
}

// SetRunnerForgeID persists the forge-assigned runner id onto the tracked
// runner state matching the given name or container id (docs/21 §5.3).
// Best-effort: a runner absent from tracked state is silently ignored —
// the next listing observation re-offers the id.
func (r *Reconciler) SetRunnerForgeID(runnerNameOrID string, forgeID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, poolMap := range r.tracked {
		for id, status := range poolMap {
			if status.ID == runnerNameOrID || status.Name == runnerNameOrID {
				status.ForgeID = forgeID
				poolMap[id] = status
				return
			}
		}
	}
}

// Start launches the background periodic audit reconciler until the context is canceled.
func (r *Reconciler) Start(ctx context.Context, interval time.Duration, onReport func(AuditReport)) error {
	if interval <= 0 {
		interval = DefaultAuditInterval
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Initial audit on start
	if report, err := r.Audit(ctx); err == nil && onReport != nil {
		onReport(report)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			report, err := r.Audit(ctx)
			if err == nil && onReport != nil {
				onReport(report)
			}
		}
	}
}
