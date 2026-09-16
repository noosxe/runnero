package db

import (
	"context"
	"fmt"
)

// DeleteRunnerPoolTombstoned deletes a runner pool and leaves a tombstone in
// the same transaction (RUN-239, docs/33 section 3.1). The tombstone is the
// ownership evidence that lets the reconcile removed-pool drain distinguish
// "pool deleted here" (drain leftovers) from "pool never existed in this
// database" (foreign supervisor on a shared engine - never touch,
// docs/33 section 3.2). Every destructive pool-delete path must go through
// this method; a pool id without a tombstone is treated as foreign.
func (d *DB) DeleteRunnerPoolTombstoned(ctx context.Context, id int64, name string) error {
	tx, err := d.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting tombstoned pool delete: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	q := d.WithTx(tx)
	if err := q.InsertPoolTombstone(ctx, InsertPoolTombstoneParams{PoolID: id, PoolName: name}); err != nil {
		return fmt.Errorf("inserting pool tombstone: %w", err)
	}
	if err := q.DeleteRunnerPool(ctx, id); err != nil {
		return fmt.Errorf("deleting runner pool: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing tombstoned pool delete: %w", err)
	}
	return nil
}
