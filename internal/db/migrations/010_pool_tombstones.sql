-- +goose Up
-- +goose StatementBegin

-- RUN-239 / docs/33 section 3.1: pool tombstones are the ownership evidence
-- that gates destructive drain decisions. Pool rows are hard-deleted from
-- runner_pools, so after a supervisor restart a tracked pool id that is
-- missing from the database is ambiguous: either the pool was deleted here
-- (drain its leftovers, docs/25 section 4.2) or this database never knew the
-- pool at all (foreign instance on a shared engine - never touch it,
-- docs/33 section 3.2). A tombstone row, written in the same transaction as
-- the delete, preserves that distinction across restarts.
CREATE TABLE pool_tombstones (
    pool_id    INTEGER PRIMARY KEY, -- the deleted runner_pools.id
    pool_name  TEXT NOT NULL,
    deleted_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE pool_tombstones;
-- +goose StatementEnd
