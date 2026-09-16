-- +goose Up
-- +goose StatementBegin

-- RUN-230 / docs/32 §8: session two-clock model, role foundation, audit provenance.
--
-- sessions gains the observability and clock columns required by the opaque
-- DB-backed session design (docs/32 §3): user_agent feeds the future session
-- list device labels; last_seen_at records activity; absolute_expires_at is
-- the unforgeable lifetime cap fixed at issuance. expires_at is redefined as
-- the sliding idle deadline.
--
-- Pre-existing JWT-era rows are NOT invalidated: they keep their original
-- ≤24h expires_at, and absolute_expires_at is seeded to the same instant, so
-- they live out their natural lifetime under the new clocks and then die
-- (docs/32 §8).
--
-- SQLite only permits CONSTANT defaults on ALTER TABLE ADD COLUMN (a
-- non-constant default is rejected outright when the table already holds
-- rows - "Cannot add a column with non-constant default"), so the clock
-- columns take a constant sentinel default that is immediately backfilled:
-- last_seen_at = created_at (last seen at issuance), absolute_expires_at =
-- expires_at (original cap). Rows created afterwards set every column
-- explicitly (CreateSession), so the sentinels never survive in data.
ALTER TABLE sessions ADD COLUMN user_agent TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN last_seen_at DATETIME NOT NULL DEFAULT '1970-01-01 00:00:00';
UPDATE sessions SET last_seen_at = created_at;
ALTER TABLE sessions ADD COLUMN absolute_expires_at DATETIME NOT NULL DEFAULT '1970-01-01 00:00:00';
UPDATE sessions SET absolute_expires_at = expires_at;

-- admin_users.role is the foundation for the procedure→role matrix
-- (docs/32 §2.3). The bootstrap admin is 'admin'; the 'viewer' bucket is
-- reserved for a future observer-users feature and no code path may assign
-- it yet.
ALTER TABLE admin_users ADD COLUMN role TEXT NOT NULL DEFAULT 'admin';

-- audit_logs.source_ip carries the client IP captured at auth decision
-- points (docs/32 §4.1); it is populated by the hardened-login phase and
-- stays NULL for action audits performed by background jobs.
ALTER TABLE audit_logs ADD COLUMN source_ip TEXT;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE audit_logs DROP COLUMN source_ip;
ALTER TABLE admin_users DROP COLUMN role;
ALTER TABLE sessions DROP COLUMN absolute_expires_at;
ALTER TABLE sessions DROP COLUMN last_seen_at;
ALTER TABLE sessions DROP COLUMN user_agent;
-- +goose StatementEnd
