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

// Reconciler maintains in-memory runner pool tracking state, reconciling it with
// host container engine state on supervisor boot and across periodic audit cycles (docs/03 §2, §3).
type Reconciler struct {
	provider ContainerProvider

	mu sync.RWMutex
	// tracked maps poolName -> map[containerID]RunnerStatus
	tracked map[string]map[string]RunnerStatus
}

// NewReconciler creates a new runner state reconciler.
func NewReconciler(provider ContainerProvider) *Reconciler {
	return &Reconciler{
		provider: provider,
		tracked:  make(map[string]map[string]RunnerStatus),
	}
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

		poolMap, exists := r.tracked[s.PoolName]
		if !exists {
			poolMap = make(map[string]RunnerStatus)
			r.tracked[s.PoolName] = poolMap
		}

		if existing, alreadyTracked := poolMap[s.ID]; alreadyTracked {
			if !s.IsBusy && existing.IsBusy {
				s.IsBusy = existing.IsBusy
			}
			if s.SpawnedAt.IsZero() && !existing.SpawnedAt.IsZero() {
				s.SpawnedAt = existing.SpawnedAt
			}
			if !s.OnDemand && existing.OnDemand {
				s.OnDemand = existing.OnDemand
			}
		} else {
			// Container was discovered on host but not yet in memory -> adopted
			report.Adopted = append(report.Adopted, s)
		}

		// Update tracked status with latest live state
		poolMap[s.ID] = s

		if s.State == "running" {
			report.Active = append(report.Active, s)
		} else {
			report.Exited = append(report.Exited, s)
		}
	}

	// Detect containers previously tracked that have disappeared from the host engine
	for poolName, poolMap := range r.tracked {
		for id := range poolMap {
			if _, stillPresent := liveMap[id]; !stillPresent {
				report.Disappeared = append(report.Disappeared, id)
				delete(poolMap, id)
			}
		}
		if len(poolMap) == 0 {
			delete(r.tracked, poolName)
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
func (r *Reconciler) TrackedPoolRunners(poolName string) []RunnerStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()

	poolMap, exists := r.tracked[poolName]
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

	poolMap, exists := r.tracked[status.PoolName]
	if !exists {
		poolMap = make(map[string]RunnerStatus)
		r.tracked[status.PoolName] = poolMap
	}
	poolMap[status.ID] = status
}

// UntrackRunner removes a terminated runner from tracking.
func (r *Reconciler) UntrackRunner(poolName, containerID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if poolMap, exists := r.tracked[poolName]; exists {
		delete(poolMap, containerID)
		if len(poolMap) == 0 {
			delete(r.tracked, poolName)
		}
	}
}

// MarkRunnerBusy updates the busy status of a runner matching the given name or ID.
func (r *Reconciler) MarkRunnerBusy(runnerNameOrID string, busy bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, poolMap := range r.tracked {
		for id, status := range poolMap {
			if status.ID == runnerNameOrID || status.Name == runnerNameOrID {
				status.IsBusy = busy
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
