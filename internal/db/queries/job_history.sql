-- name: CreateJobHistory :one
INSERT INTO job_history (
    pool_id,
    runner_name,
    status,
    queued_at,
    started_at,
    completed_at,
    log_retention_path,
    source
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?
) RETURNING *;

-- name: GetJobHistoryById :one
SELECT * FROM job_history
WHERE id = ? LIMIT 1;

-- name: ListJobHistory :many
SELECT * FROM job_history
ORDER BY id DESC
LIMIT ? OFFSET ?;

-- name: ListJobHistoryByPoolId :many
SELECT * FROM job_history
WHERE pool_id = ?
ORDER BY id DESC
LIMIT ? OFFSET ?;

-- name: UpdateJobHistoryStatus :one
UPDATE job_history
SET status = ?,
    completed_at = ?,
    log_retention_path = ?
WHERE id = ?
RETURNING *;

-- name: DeleteJobHistoryOlderThan :exec
DELETE FROM job_history
WHERE completed_at < ?;

-- name: CountJobHistory :one
SELECT COUNT(*) FROM job_history;

-- name: CountJobHistoryByPoolId :one
SELECT COUNT(*) FROM job_history
WHERE pool_id = ?;

-- name: SearchJobHistory :many
SELECT * FROM job_history
WHERE (sqlc.arg('pool_id') = 0 OR pool_id = sqlc.arg('pool_id'))
  AND (sqlc.arg('search') = '' OR runner_name LIKE '%' || sqlc.arg('search') || '%')
  AND (sqlc.arg('status') = '' OR status = sqlc.arg('status'))
ORDER BY id DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: CountSearchJobHistory :one
SELECT COUNT(*) FROM job_history
WHERE (sqlc.arg('pool_id') = 0 OR pool_id = sqlc.arg('pool_id'))
  AND (sqlc.arg('search') = '' OR runner_name LIKE '%' || sqlc.arg('search') || '%')
  AND (sqlc.arg('status') = '' OR status = sqlc.arg('status'));

-- name: PruneJobHistoryOlderThan :many
DELETE FROM job_history
WHERE (completed_at IS NOT NULL AND completed_at < ?)
   OR (completed_at IS NULL AND created_at < ?)
RETURNING id, log_retention_path;

-- name: GetJobStatsSince :one
SELECT
    COUNT(*) as total_jobs,
    COALESCE(SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END), 0) as successful_jobs,
    COALESCE(SUM(CASE WHEN status = 'failure' OR status = 'failed' THEN 1 ELSE 0 END), 0) as failed_jobs,
    COALESCE(SUM(CASE WHEN status IN ('success', 'failure', 'cancelled', 'timeout') THEN 1 ELSE 0 END), 0) as known_outcome_jobs,
    COALESCE(SUM(CASE WHEN queued_at IS NOT NULL THEN 1 ELSE 0 END), 0) as queue_timed_jobs,
    COALESCE(AVG(CASE WHEN started_at IS NOT NULL AND queued_at IS NOT NULL THEN (CAST(strftime('%s', replace(substr(started_at, 1, 19), 'T', ' ')) AS REAL) - CAST(strftime('%s', replace(substr(queued_at, 1, 19), 'T', ' ')) AS REAL)) END), 0.0) as avg_queue_seconds,
    COALESCE(AVG(CASE WHEN completed_at IS NOT NULL AND started_at IS NOT NULL THEN (CAST(strftime('%s', replace(substr(completed_at, 1, 19), 'T', ' ')) AS REAL) - CAST(strftime('%s', replace(substr(started_at, 1, 19), 'T', ' ')) AS REAL)) END), 0.0) as avg_runtime_seconds
FROM job_history
WHERE created_at >= ?;
-- name: GetHourlyJobStatsSince :many
SELECT
    strftime('%Y-%m-%dT%H:00:00Z', created_at) as bucket_hour,
    COUNT(*) as total_jobs,
    COALESCE(SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END), 0) as successful_jobs,
    COALESCE(SUM(CASE WHEN status = 'failure' OR status = 'failed' THEN 1 ELSE 0 END), 0) as failed_jobs,
    COALESCE(SUM(CASE WHEN status IN ('success', 'failure', 'cancelled', 'timeout') THEN 1 ELSE 0 END), 0) as known_outcome_jobs,
    COALESCE(SUM(CASE WHEN queued_at IS NOT NULL THEN 1 ELSE 0 END), 0) as queue_timed_jobs,
    COALESCE(AVG(CASE WHEN started_at IS NOT NULL AND queued_at IS NOT NULL THEN (CAST(strftime('%s', replace(substr(started_at, 1, 19), 'T', ' ')) AS REAL) - CAST(strftime('%s', replace(substr(queued_at, 1, 19), 'T', ' ')) AS REAL)) END), 0.0) as avg_queue_seconds,
    COALESCE(AVG(CASE WHEN completed_at IS NOT NULL AND started_at IS NOT NULL THEN (CAST(strftime('%s', replace(substr(completed_at, 1, 19), 'T', ' ')) AS REAL) - CAST(strftime('%s', replace(substr(started_at, 1, 19), 'T', ' ')) AS REAL)) END), 0.0) as avg_runtime_seconds
FROM job_history
WHERE created_at >= ?
GROUP BY bucket_hour
ORDER BY bucket_hour ASC;

-- name: GetOpenJobRow :one
SELECT id FROM job_history
WHERE pool_id = ? AND runner_name = ? AND completed_at IS NULL
LIMIT 1;

-- name: OpenJobLifecycleRow :one
INSERT INTO job_history (
    pool_id,
    runner_name,
    status,
    started_at,
    source
) VALUES (
    ?, ?, 'running', ?, 'transition'
) RETURNING *;

-- name: CloseOpenJobRow :execrows
UPDATE job_history
SET completed_at = ?,
    status = ?,
    log_retention_path = ?
WHERE pool_id = ?
  AND runner_name = ?
  AND completed_at IS NULL;

-- name: CloseAllOpenJobsInterrupted :execrows
UPDATE job_history
SET completed_at = ?,
    status = 'interrupted'
WHERE completed_at IS NULL;

-- name: CloseStaleOpenJobsSince :execrows
UPDATE job_history
SET completed_at = ?,
    status = 'interrupted'
WHERE completed_at IS NULL
  AND started_at IS NOT NULL
  AND started_at < ?
  AND pool_id = ?;
