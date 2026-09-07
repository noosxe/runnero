-- +goose Up
-- +goose StatementBegin

-- Job lifecycle recording (docs/21): extend job_history with external job
-- metadata, a source discriminator, and the lifecycle status vocabulary.
-- SQLite cannot alter CHECK constraints in place, so recreate the table.
CREATE TABLE job_history_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    pool_id INTEGER NOT NULL,
    runner_name TEXT NOT NULL,
    status TEXT NOT NULL CHECK(status IN ('queued', 'running', 'success', 'failure', 'cancelled', 'timeout', 'completed', 'interrupted')),
    queued_at DATETIME,
    started_at DATETIME,
    completed_at DATETIME,
    log_retention_path TEXT,
    job_id BIGINT,
    run_id BIGINT,
    workflow_name TEXT,
    head_branch TEXT,
    head_sha TEXT,
    source TEXT NOT NULL DEFAULT 'transition' CHECK(source IN ('transition', 'webhook', 'timeout')),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY(pool_id) REFERENCES runner_pools(id) ON DELETE CASCADE
);

-- Preserve existing rows. Legacy rows only ever came from the hung-runner
-- timeout path (docs/21 §1), so they backfill as source 'timeout'. Their
-- statuses are all within the new CHECK vocabulary.
INSERT INTO job_history_new (id, pool_id, runner_name, status, queued_at, started_at, completed_at, log_retention_path, source, created_at)
SELECT id, pool_id, runner_name, status, queued_at, started_at, completed_at, log_retention_path, 'timeout', created_at
FROM job_history;

DROP TABLE job_history;
ALTER TABLE job_history_new RENAME TO job_history;

-- At most one OPEN row per runner slot: the recorder upserts against this
-- invariant (docs/21 §5.5). Rows awaiting runner assignment (empty name,
-- webhook 'queued' rows, Phase 2) are exempt.
CREATE UNIQUE INDEX idx_job_history_open_runner
    ON job_history(pool_id, runner_name)
    WHERE completed_at IS NULL AND runner_name != '';

CREATE INDEX idx_job_history_pool_created ON job_history(pool_id, created_at);
CREATE INDEX idx_job_history_job_id ON job_history(job_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

CREATE TABLE job_history_old (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    pool_id INTEGER NOT NULL,
    runner_name TEXT NOT NULL,
    status TEXT NOT NULL CHECK(status IN ('success', 'failure', 'cancelled', 'timeout')),
    queued_at DATETIME,
    started_at DATETIME,
    completed_at DATETIME,
    log_retention_path TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY(pool_id) REFERENCES runner_pools(id) ON DELETE CASCADE
);

-- Rows with lifecycle-only statuses (queued/running/completed/interrupted)
-- are not representable in the legacy schema and are intentionally dropped.
INSERT INTO job_history_old (id, pool_id, runner_name, status, queued_at, started_at, completed_at, log_retention_path, created_at)
SELECT id, pool_id, runner_name, status, queued_at, started_at, completed_at, log_retention_path, created_at
FROM job_history
WHERE status IN ('success', 'failure', 'cancelled', 'timeout');

DROP TABLE job_history;
ALTER TABLE job_history_old RENAME TO job_history;

-- +goose StatementEnd
