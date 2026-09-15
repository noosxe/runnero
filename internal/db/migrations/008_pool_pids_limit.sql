-- +goose Up
-- +goose StatementBegin

-- RUN-148: configurable per-pool PIDs cap for runner containers.
-- Semantics: NULL = legacy unset (no cap — preserves pre-RUN-148 behavior),
-- 0 = explicit unlimited (opt-out), otherwise the maximum number of
-- processes the container may run (HostConfig.PidsLimit). Written through
-- the API it is always non-NULL (0 included); NULL exists only on rows that
-- predate the migration.
ALTER TABLE runner_pools ADD COLUMN pids_limit INTEGER;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE runner_pools DROP COLUMN pids_limit;
-- +goose StatementEnd
