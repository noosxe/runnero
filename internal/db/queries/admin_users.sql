-- name: CreateAdminUser :one
INSERT INTO admin_users (
    username,
    password_hash,
    role
) VALUES (
    ?, ?, ?
) RETURNING *;

-- name: GetAdminUserById :one
SELECT * FROM admin_users
WHERE id = ? LIMIT 1;

-- name: GetAdminUserByUsername :one
SELECT * FROM admin_users
WHERE username = ? LIMIT 1;

-- name: ListAdminUsers :many
SELECT * FROM admin_users
ORDER BY id ASC;

-- name: CountAdminUsers :one
SELECT COUNT(*) FROM admin_users;

-- name: CountAdminRoleUsers :one
SELECT COUNT(*) FROM admin_users
WHERE role = 'admin';

-- name: UpdateAdminPassword :one
UPDATE admin_users
SET password_hash = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING *;

-- name: UpdateAdminRole :one
UPDATE admin_users
SET role = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING *;

-- name: DeleteAdminUser :exec
DELETE FROM admin_users
WHERE id = ?;
