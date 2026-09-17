-- WebAuthn credential storage (RUN-247, docs/34 sections 3.6 and 6). Rows
-- are public material only: COSE public keys, credential IDs, counters and
-- flags. The credential_id UNIQUE column is the passwordless-login identity
-- lookup (docs/34 section 3.2); ownership-scoped statements carry user_id so
-- a caller can only ever touch their own rows (docs/34 section 3.9).

-- name: CreateWebauthnCredential :one
INSERT INTO webauthn_credentials (
    user_id, name, credential_id, public_key, aaguid, attestation_type,
    transports, sign_count, backup_eligible, backup_state, clone_warning
) VALUES (
    ?, ?, ?, ?, ?, ?,
    ?, ?, ?, ?, ?
)
RETURNING *;

-- name: GetWebauthnCredentialById :one
-- The passwordless-login identity lookup: raw credential ID to row (docs/34
-- section 3.2).
SELECT * FROM webauthn_credentials WHERE credential_id = ?;

-- name: GetWebauthnCredentialByIdAndUserId :one
-- Ownership-scoped single-row fetch for Rename/Delete pre-checks.
SELECT * FROM webauthn_credentials WHERE id = ? AND user_id = ?;

-- name: ListWebauthnCredentialsByUserId :many
SELECT * FROM webauthn_credentials WHERE user_id = ? ORDER BY created_at, id;

-- name: CountWebauthnCredentialsByUserId :one
SELECT count(*) FROM webauthn_credentials WHERE user_id = ?;

-- name: RenameWebauthnCredential :execrows
UPDATE webauthn_credentials SET name = ? WHERE id = ? AND user_id = ?;

-- name: DeleteWebauthnCredentialByIdAndUserId :execrows
DELETE FROM webauthn_credentials WHERE id = ? AND user_id = ?;

-- name: UpdateWebauthnCredentialAssertionState :exec
-- Called after every successful assertion (docs/34 section 3.8): the
-- library's counter policy result plus the response's backup flags and the
-- last-use stamp.
UPDATE webauthn_credentials
SET sign_count = ?, backup_eligible = ?, backup_state = ?, last_used_at = ?
WHERE credential_id = ?;

-- name: SetWebauthnCredentialCloneWarning :exec
-- Fail-closed flag on a non-increasing sign counter (docs/34 section 3.8):
-- a flagged credential refuses all further assertions until removed.
UPDATE webauthn_credentials SET clone_warning = 1 WHERE credential_id = ?;
