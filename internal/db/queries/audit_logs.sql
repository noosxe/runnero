-- name: CreateAuditLog :one
INSERT INTO audit_logs (
    user_id,
    action,
    resource_type,
    resource_id,
    details,
    source_ip
) VALUES (
    ?, ?, ?, ?, ?, ?
) RETURNING *;

-- name: GetAuditLogById :one
SELECT * FROM audit_logs
WHERE id = ? LIMIT 1;

-- name: ListAuditLogs :many
SELECT * FROM audit_logs
ORDER BY id DESC
LIMIT ? OFFSET ?;

-- name: ListAuditLogsByUserId :many
SELECT * FROM audit_logs
WHERE user_id = ?
ORDER BY id DESC
LIMIT ? OFFSET ?;

-- name: CountAuditLogs :one
SELECT COUNT(*) FROM audit_logs;
