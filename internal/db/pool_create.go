package db

import (
	"context"
	"fmt"
)

// PoolCreate carries the write legs of a pool create (RUN-163, docs/22 §5.5):
// the pool row, an optional renovate config, and the seed target set. A nil
// Renovate creates no config row; Targets always lands verbatim — the caller
// supplies the repository-URL fallback and filtering, so an empty slice means
// "no targets" and is honoured as such.
type PoolCreate struct {
	Pool     CreateRunnerPoolParams
	Renovate *CreateRenovateConfigParams
	Targets  []string
}

// CreatePool persists a pool create atomically (RUN-163): the pool row, the
// optional renovate config, and the seed targets either all land or none do.
// The previous sequential writes swallowed per-leg errors (`_, _ =`), so a
// failed renovate or target insert silently produced a pool without them.
func (d *DB) CreatePool(ctx context.Context, req PoolCreate) (RunnerPool, error) {
	tx, err := d.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return RunnerPool{}, fmt.Errorf("beginning pool create transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := d.WithTx(tx)

	created, err := q.CreateRunnerPool(ctx, req.Pool)
	if err != nil {
		return RunnerPool{}, fmt.Errorf("creating runner pool: %w", err)
	}

	if req.Renovate != nil {
		cfg := *req.Renovate
		cfg.PoolID = created.ID
		if _, err := q.CreateRenovateConfig(ctx, cfg); err != nil {
			return RunnerPool{}, fmt.Errorf("creating renovate config: %w", err)
		}
	}

	for _, t := range req.Targets {
		if _, err := q.AddPoolTarget(ctx, AddPoolTargetParams{
			PoolID:    created.ID,
			TargetUrl: t,
		}); err != nil {
			return RunnerPool{}, fmt.Errorf("adding pool target %q: %w", t, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return RunnerPool{}, fmt.Errorf("committing pool create: %w", err)
	}
	return created, nil
}
