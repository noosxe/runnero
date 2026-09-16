-- Durable login rate limiter state (RUN-238, docs/32 section 4.2): one row
-- per failed login attempt, keyed by username+IP (rateLimitKey). The limiter
-- writes through on every failure, deletes a key's rows on successful login,
-- and reloads in-window rows on boot so a restart cannot reset a lockout.

-- name: InsertLoginRateFailure :exec
INSERT INTO login_rate_failures (rate_key, failed_at)
VALUES (?, ?);

-- name: DeleteLoginRateFailuresByKey :exec
DELETE FROM login_rate_failures WHERE rate_key = ?;

-- name: DeleteLoginRateFailuresBefore :exec
DELETE FROM login_rate_failures WHERE failed_at <= ?;

-- name: ListLoginRateFailuresSince :many
SELECT rate_key, failed_at FROM login_rate_failures WHERE failed_at > ?;
