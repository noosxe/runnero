-- Pool tombstones (RUN-239, docs/33 section 3.1): ownership evidence left
-- behind by pool deletions. The reconcile removed-pool drain only proceeds
-- for pool ids that carry a tombstone; ids without one belong to a foreign
-- supervisor instance on a shared engine and are never touched.

-- name: InsertPoolTombstone :exec
-- Idempotent: a pool id can only be deleted once (AUTOINCREMENT ids are
-- never reused), but the upsert keeps the write safe under replay.
INSERT INTO pool_tombstones (pool_id, pool_name)
VALUES (?, ?)
ON CONFLICT(pool_id) DO UPDATE SET
    pool_name = excluded.pool_name,
    deleted_at = CURRENT_TIMESTAMP;

-- name: PoolTombstoneExists :one
SELECT EXISTS (
    SELECT 1 FROM pool_tombstones WHERE pool_id = ?
) AS present;
