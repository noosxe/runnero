-- name: CreateSession :one
INSERT INTO sessions (
    user_id,
    token_hash,
    expires_at,
    absolute_expires_at,
    user_agent
) VALUES (
    ?, ?, ?, ?, ?
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

-- name: DeleteExpiredSessions :exec
DELETE FROM sessions
WHERE expires_at < ?;
