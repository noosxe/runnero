-- +goose Up
-- +goose StatementBegin

-- RUN-238 / docs/32 section 4.2: durable brute-force lockout state. The login
-- rate limiter is in-memory (sliding 15-minute failure window per
-- username+IP), so a supervisor restart used to wipe every lockout and reset
-- an attacker's exponential backoff. Each failed login now also writes one
-- row here (write-through, best-effort - the in-memory guard never depends
-- on the database); on boot the limiter reloads rows inside the window, so a
-- restart no longer undoes a lockout. Rows outside the window are dead
-- weight and pruned (boot and per-write), keeping the table bounded.
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
