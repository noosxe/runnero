-- +goose Up
-- +goose StatementBegin

-- RUN-277 (finding QUAL-05): runner_pools.repository_url was a live, synced
-- duplicate of the pool's first pool_targets row (the write path backfilled
-- it from TargetUrls[0] since migration 003). Every read now goes through
-- pool_targets; the legacy column is dropped. No index, trigger, or view
-- references the column, so a plain DROP COLUMN is safe (SQLite >= 3.35).
ALTER TABLE runner_pools DROP COLUMN repository_url;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Restore the column and re-derive it from the first (lowest-id) target so
-- downgrades keep the pre-RUN-277 invariant repository_url == targets[0].
ALTER TABLE runner_pools ADD COLUMN repository_url TEXT NOT NULL DEFAULT '';
UPDATE runner_pools
SET repository_url = COALESCE((
    SELECT pt.target_url
    FROM pool_targets pt
    WHERE pt.pool_id = runner_pools.id
    ORDER BY pt.id
    LIMIT 1
), '');

-- +goose StatementEnd
