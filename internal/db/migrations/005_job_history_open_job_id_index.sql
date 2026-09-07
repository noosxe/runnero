-- +goose Up
-- +goose StatementBegin

-- Webhook enrichment (docs/21 §5.5): one row per external job id. Webhook
-- events upsert against this constraint. NULL job ids (transition rows without
-- webhook enrichment) are exempt — SQLite unique indexes admit multiple NULLs.
-- Replaces the non-unique lookup index created by migration 004.
DROP INDEX IF EXISTS idx_job_history_job_id;
CREATE UNIQUE INDEX idx_job_history_job_id
    ON job_history(job_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_job_history_job_id;
CREATE INDEX idx_job_history_job_id
    ON job_history(job_id);

-- +goose StatementEnd
