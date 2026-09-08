-- name: CreateRunnerPool :one
INSERT INTO runner_pools (
    name,
    provider,
    repository_url,
    scope,
    auth_profile_id,
    min_idle_runners,
    max_concurrency,
    labels,
    runner_image,
    allow_docker,
    max_runner_lifetime_seconds,
    cpu_limit,
    memory_limit,
    poll_fallback,
    poll_interval_seconds
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
) RETURNING *;

-- name: GetRunnerPoolById :one
SELECT * FROM runner_pools
WHERE id = ? LIMIT 1;

-- name: GetRunnerPoolByName :one
SELECT * FROM runner_pools
WHERE name = ? LIMIT 1;

-- name: ListRunnerPools :many
SELECT * FROM runner_pools
ORDER BY name ASC;

-- name: CountRunnerPools :one
SELECT COUNT(*) FROM runner_pools;

-- name: UpdateRunnerPool :one
UPDATE runner_pools
SET name = ?,
    provider = ?,
    repository_url = ?,
    scope = ?,
    auth_profile_id = ?,
    min_idle_runners = ?,
    max_concurrency = ?,
    labels = ?,
    runner_image = ?,
    allow_docker = ?,
    max_runner_lifetime_seconds = ?,
    cpu_limit = ?,
    memory_limit = ?,
    poll_fallback = ?,
    poll_interval_seconds = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING *;

-- name: DeleteRunnerPool :exec
DELETE FROM runner_pools
WHERE id = ?;
