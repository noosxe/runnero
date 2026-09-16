-- name: CreateSession :one
-- Every column is set explicitly: SQLite cannot ALTER a non-constant
-- DEFAULT onto a populated table (migration 009), so last_seen_at carries
-- a sentinel default that must never survive in data.
INSERT INTO sessions (
    user_id,
    token_hash,
    expires_at,
    absolute_expires_at,
    user_agent,
    last_seen_at
) VALUES (
    ?, ?, ?, ?, ?, ?
) RETURNING *;

-- name: TouchSession :exec
-- Sliding renewal (docs/32 section 3.3): extend the idle deadline and record
-- the activity instant. Callers clamp expires_at at absolute_expires_at before
-- writing: the cap is never extended.
UPDATE sessions
SET expires_at = ?,
    last_seen_at = CURRENT_TIMESTAMP
WHERE token_hash = ?;

-- name: GetSessionByTokenHash :one
SELECT * FROM sessions
WHERE token_hash = ? LIMIT 1;

-- name: ListSessionsByUserId :many
SELECT * FROM sessions
WHERE user_id = ?
ORDER BY created_at DESC;

-- name: DeleteSessionByTokenHash :exec
DELETE FROM sessions
WHERE token_hash = ?;

-- name: DeleteSessionsByUserId :exec
DELETE FROM sessions
WHERE user_id = ?;

-- name: PurgeExpiredSessions :execrows
-- Hourly maintenance (docs/32 section 5.2): delete sessions past either
-- clock - the sliding idle deadline or the fixed absolute cap.
DELETE FROM sessions
WHERE expires_at < sqlc.arg(now) OR absolute_expires_at < sqlc.arg(now);

-- name: DeleteSessionByIdAndUserId :execrows
-- Revoke one of the caller's sessions. The user_id guard is the query-level
-- ownership check (docs/32 section 3.5): a row owned by someone else never
-- matches, so foreign ids answer zero rows.
DELETE FROM sessions
WHERE id = ? AND user_id = ?;

-- name: DeleteOtherSessionsByUserId :execrows
-- Revoke every session owned by the caller except the current row.
DELETE FROM sessions
WHERE user_id = ? AND id != ?;
