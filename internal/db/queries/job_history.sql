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

-- name: UpsertWebhookQueuedJob :one
-- Webhook 'queued' upsert keyed by the external job id (docs/21 section 5.5).
-- Conflicts only fill in missing metadata: status, runner assignment, and the
-- original queued_at are never rewritten by a (re)delivery.
INSERT INTO job_history (
    pool_id,
    job_id,
    run_id,
    workflow_name,
    head_branch,
    head_sha,
    status,
    queued_at,
    source,
    runner_name
) VALUES (
    ?, ?, ?, ?, ?, ?, 'queued', ?, 'webhook', ''
)
ON CONFLICT(job_id) DO UPDATE SET
    queued_at = COALESCE(job_history.queued_at, excluded.queued_at),
    run_id = COALESCE(excluded.run_id, job_history.run_id),
    workflow_name = COALESCE(excluded.workflow_name, job_history.workflow_name),
    head_branch = COALESCE(excluded.head_branch, job_history.head_branch),
    head_sha = COALESCE(excluded.head_sha, job_history.head_sha)
WHERE job_history.completed_at IS NULL
RETURNING id;

-- name: GetOpenWebhookJobByID :one
SELECT id, runner_name, queued_at FROM job_history
WHERE job_id = ? AND completed_at IS NULL
LIMIT 1;

-- name: PromoteWebhookStubToRunning :execrows
-- Webhook 'in_progress' with a queued stub but no transition row: promote the
-- stub in place (docs/21 section 5.5 merge rule 2).
UPDATE job_history
SET runner_name = ?,
    status = 'running',
    started_at = COALESCE(started_at, ?)
WHERE id = ? AND completed_at IS NULL;

-- name: AttachWebhookToTransition :execrows
-- Webhook 'in_progress' arriving after (or before) the busy-sync transition
-- row: attach the external identity and timestamps to the runner's open row
-- (docs/21 section 5.5 merge rule 3). queued_at is adopted from the forge event -
-- real data, never fabricated.
UPDATE job_history
SET job_id = ?,
    status = 'running',
    queued_at = COALESCE(queued_at, ?),
    started_at = COALESCE(started_at, ?),
    run_id = COALESCE(?, run_id),
    workflow_name = COALESCE(?, workflow_name),
    head_branch = COALESCE(?, head_branch),
    head_sha = COALESCE(?, head_sha)
WHERE pool_id = ? AND runner_name = ? AND completed_at IS NULL;

-- name: InsertWebhookRunningJob :exec
-- Webhook 'in_progress' with no pre-existing row at all (docs/21 section 5.5 merge rule 4).
INSERT INTO job_history (
    pool_id,
    runner_name,
    job_id,
    run_id,
    workflow_name,
    head_branch,
    head_sha,
    status,
    started_at,
    queued_at,
    source
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, 'running', ?, ?, 'webhook'
);

-- name: CloseOpenWebhookJobByID :execrows
-- Webhook 'completed' close keyed by the external job id; adopts the runner
-- name when the closed row predates assignment (docs/21 section 5.5).
UPDATE job_history
SET completed_at = ?,
    status = ?,
    runner_name = CASE WHEN COALESCE(runner_name, '') = '' THEN ? ELSE runner_name END
WHERE job_id = ? AND completed_at IS NULL;

-- name: DeleteOpenTransitionRowsByRunner :execrows
-- Duplicate cleanup after closing by external job id: a poll-opened transition
-- row (empty job id) for the same runner would double-count the job.
DELETE FROM job_history
WHERE pool_id = ? AND runner_name = ? AND completed_at IS NULL
  AND (job_id IS NULL OR job_id = 0);

-- name: CloseOpenRowByRunnerEnrichJobID :execrows
-- Webhook 'completed' fallback: no row carries the external id (poll-opened
-- transition row only) - close it and enrich it with the job id (docs/21 section 5.5).
UPDATE job_history
SET completed_at = ?,
    status = ?,
    job_id = ?
WHERE pool_id = ? AND runner_name = ? AND completed_at IS NULL
  AND (job_id IS NULL OR job_id = 0);

-- name: DeleteJobRowByID :exec
DELETE FROM job_history
WHERE id = ?;
