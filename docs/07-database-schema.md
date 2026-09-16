# Database Schema Design

This document defines the SQLite database schemas managed via **Goose** migrations and **SQLc**. All schemas are finalized during the design phase.

## `001_initial_schema.sql`

```sql
-- +goose Up
-- +goose StatementBegin

CREATE TABLE admin_users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    username TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL, -- Hashed via bcrypt (cost configurable, default 12)
    role TEXT NOT NULL DEFAULT 'admin', -- 'admin'; 'viewer' reserved for a future observer-users design
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE sessions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL,
    token_hash TEXT NOT NULL UNIQUE, -- SHA-256 hash of the opaque 32-byte random token (docs/32 section 3.2)
    expires_at DATETIME NOT NULL, -- sliding idle deadline (docs/32 section 3.1)
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    absolute_expires_at DATETIME NOT NULL, -- fixed lifetime cap, never extended
    user_agent TEXT NOT NULL DEFAULT '',   -- device label source for the session list
    last_seen_at DATETIME NOT NULL DEFAULT '1970-01-01 00:00:00', -- slides with renewal; set explicitly on insert (SQLite ALTER forbids non-constant defaults)
    FOREIGN KEY(user_id) REFERENCES admin_users(id) ON DELETE CASCADE
);

An hourly maintenance sweep (docs/32 section 5.2) deletes session rows past
either expiry clock; audit rows older than the retention horizon
(`SUPERVISOR_AUDIT_RETENTION`, default 90d) are purged by the same sweep.

CREATE TABLE auth_profiles (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    auth_method TEXT NOT NULL CHECK(auth_method IN ('github_app', 'gitea_token', 'forgejo_token', 'pat')),
    app_id INTEGER,
    private_key_encrypted TEXT, -- AES-256 Encrypted
    token_encrypted TEXT,       -- AES-256 Encrypted (For PAT / Gitea / Forgejo)
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE runner_pools (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    provider TEXT NOT NULL CHECK(provider IN ('github', 'gitea', 'forgejo')),
    repository_url TEXT NOT NULL,
    scope TEXT NOT NULL DEFAULT 'repo' CHECK(scope IN ('repo', 'org', 'global')),
    auth_profile_id INTEGER NOT NULL,
    min_idle_runners INTEGER NOT NULL DEFAULT 1,
    max_concurrency INTEGER NOT NULL DEFAULT 5,
    labels TEXT NOT NULL, -- JSON array, e.g. '["self-hosted","linux","arm64"]'
    runner_image TEXT NOT NULL,
    allow_docker BOOLEAN NOT NULL DEFAULT 0,
    max_runner_lifetime_seconds INTEGER NOT NULL DEFAULT 7200, -- max busy wall-clock per job, anchored at first pickup (docs/23)
    cpu_limit TEXT,
    memory_limit TEXT,
    memory_swap_limit TEXT, -- RUN-147: total mem+swap (Docker MemorySwap semantics); NULL = daemon default 2x, '-1' = unlimited
    pids_limit INTEGER, -- RUN-148: max processes per container (Docker PidsLimit semantics); NULL = legacy unset (no cap), 0 = explicit unlimited
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY(auth_profile_id) REFERENCES auth_profiles(id)
);

CREATE TABLE renovate_configs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    pool_id INTEGER NOT NULL UNIQUE,
    enabled BOOLEAN NOT NULL DEFAULT 0,
    cron_schedule TEXT,
    image TEXT NOT NULL DEFAULT 'renovate/renovate:latest',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY(pool_id) REFERENCES runner_pools(id) ON DELETE CASCADE
);

CREATE TABLE job_history (
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


> **Lifecycle extension (docs/21, shipped):** `job_history` carries external job
> metadata columns (`job_id`, `run_id`, `workflow_name`, `head_branch`, `head_sha`), a
> `source` discriminator (`transition` / `webhook` / `timeout`), and the extended
> status vocabulary (`queued`, `running`, `completed`, `interrupted` added to the
> legacy outcome set). A partial unique index enforces at most one open row per
> `(pool_id, runner_name)`; the busy-state transition recorder opens and closes
> these rows, boot recovery closes stale ones as `interrupted`. See
> [docs/21-job-history-recording.md](21-job-history-recording.md).

CREATE TABLE audit_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER,
    action TEXT NOT NULL,        -- e.g., 'pool.create', 'auth_profile.delete', 'login.success'
    resource_type TEXT,          -- e.g., 'runner_pool', 'auth_profile'
    resource_id INTEGER,
    details TEXT,                -- JSON blob with contextual data
    source_ip TEXT,              -- client IP captured at auth decision points (docs/32 section 4.1)
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY(user_id) REFERENCES admin_users(id) ON DELETE SET NULL
);

CREATE TABLE app_settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Seed default global settings
INSERT INTO app_settings (key, value) VALUES
    ('total_allowed_runners', '20'),
    ('total_idle_warm_pool', '5'),
    ('shutdown_timeout_seconds', '300'),
    ('job_retention_days', '30');

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE app_settings;
DROP TABLE audit_logs;
DROP TABLE job_history;
DROP TABLE renovate_configs;
DROP TABLE runner_pools;
DROP TABLE auth_profiles;
DROP TABLE sessions;
DROP TABLE admin_users;
-- +goose StatementEnd
```

## `002_renovate_runs.sql`

```sql
-- +goose Up
-- +goose StatementBegin

CREATE TABLE renovate_runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    pool_id INTEGER NOT NULL,
    status TEXT NOT NULL CHECK(status IN ('running', 'success', 'failure')),
    started_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME,
    summary TEXT NOT NULL DEFAULT '',
    container_id TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY(pool_id) REFERENCES runner_pools(id) ON DELETE CASCADE
);

CREATE INDEX idx_renovate_runs_pool_started ON renovate_runs(pool_id, started_at DESC);
CREATE INDEX idx_renovate_runs_container ON renovate_runs(container_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE renovate_runs;
-- +goose StatementEnd
```

## `003_pool_targets.sql`

Supports multi-target runner pools where a single pool can be linked to multiple repositories OR multiple organizations (see [14-multi-target-pool-wizard.md](14-multi-target-pool-wizard.md)).

```sql
-- +goose Up
-- +goose StatementBegin

CREATE TABLE pool_targets (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    pool_id INTEGER NOT NULL,
    target_url TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY(pool_id) REFERENCES runner_pools(id) ON DELETE CASCADE,
    UNIQUE(pool_id, target_url)
);

CREATE INDEX idx_pool_targets_pool_id ON pool_targets(pool_id);
CREATE INDEX idx_pool_targets_target_url ON pool_targets(target_url);

-- Backfill legacy single-target runner_pools into pool_targets
INSERT INTO pool_targets (pool_id, target_url)
SELECT id, repository_url FROM runner_pools WHERE repository_url != '';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE pool_targets;
-- +goose StatementEnd
```

## `010_pool_tombstones.sql`

Pool tombstones are the ownership evidence for destructive drain decisions
(RUN-239, docs/33 section 3.1): the removed-pool drain only proceeds for pool
ids this database actually deleted. A pool id without a tombstone belongs to
a foreign supervisor instance on a shared engine and is never touched. Rows
are written transactionally with the pool delete (`DeleteRunnerPoolTombstoned`)
and survive restarts by design.

```sql
-- +goose Up
-- +goose StatementBegin
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
```

## `011_login_rate_failures.sql`

Durable brute-force lockout state for the login rate limiter (RUN-238,
docs/32 section 4.2). The in-memory sliding window survives only until a
restart, which used to let an attacker reset their exponential backoff by
inducing or waiting for one. Every failed login now also writes one row
here (write-through, best-effort — the in-memory guard never depends on
the database); on boot the limiter reloads rows inside the 15-minute
window. A successful login deletes the key's rows so a restart cannot
resurrect a spent lockout; rows outside the window are pruned at boot and
per write, keeping the table bounded.

```sql
-- +goose Up
-- +goose StatementBegin
CREATE TABLE login_rate_failures (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    rate_key  TEXT NOT NULL,
    failed_at DATETIME NOT NULL
);

CREATE INDEX idx_login_rate_failures_key_failed_at
    ON login_rate_failures (rate_key, failed_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE login_rate_failures;
-- +goose StatementEnd
```
