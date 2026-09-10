package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// PoolUpdate carries the write legs of a pool update (RUN-129, docs/22 §5.5):
// the pool row itself, an optional renovate-config upsert, and an optional
// full target rewrite. A nil Renovate leaves the renovate config untouched; a
// nil Targets leaves pool_targets untouched. A non-nil Targets replaces the
// whole set (a valid pool always carries at least one target, so an empty
// slice is unambiguous).
type PoolUpdate struct {
	Pool     UpdateRunnerPoolParams
	Renovate *UpdateRenovateConfigParams
	Targets  []string
}

// UpdatePool persists a pool update atomically (RUN-129): the pool row, the
// renovate-config upsert (update, falling back to create when the pool has no
// config row yet), and the target rewrite either all land or none do. The
// previous sequential writes left mixed state on a mid-sequence failure —
// e.g. a renamed pool still carrying the old targets.
func (d *DB) UpdatePool(ctx context.Context, req PoolUpdate) (RunnerPool, error) {
	tx, err := d.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return RunnerPool{}, fmt.Errorf("beginning pool update transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := d.WithTx(tx)

	updated, err := q.UpdateRunnerPool(ctx, req.Pool)
	if err != nil {
		return RunnerPool{}, fmt.Errorf("updating runner pool: %w", err)
	}

	if req.Renovate != nil {
		up := *req.Renovate
		up.PoolID = updated.ID
		if _, err := q.UpdateRenovateConfig(ctx, up); err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return RunnerPool{}, fmt.Errorf("updating renovate config: %w", err)
			}
			if _, cerr := q.CreateRenovateConfig(ctx, CreateRenovateConfigParams{
				PoolID:       updated.ID,
				Enabled:      up.Enabled,
				CronSchedule: up.CronSchedule,
				Image:        up.Image,
			}); cerr != nil {
				return RunnerPool{}, fmt.Errorf("creating renovate config: %w", cerr)
			}
		}
	}

	if req.Targets != nil {
		if err := q.DeletePoolTargetsByPoolId(ctx, updated.ID); err != nil {
			return RunnerPool{}, fmt.Errorf("replacing pool targets: %w", err)
		}
		for _, t := range req.Targets {
			if _, err := q.AddPoolTarget(ctx, AddPoolTargetParams{
				PoolID:    updated.ID,
				TargetUrl: t,
			}); err != nil {
				return RunnerPool{}, fmt.Errorf("adding pool target %q: %w", t, err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return RunnerPool{}, fmt.Errorf("committing pool update: %w", err)
	}
	return updated, nil
}
