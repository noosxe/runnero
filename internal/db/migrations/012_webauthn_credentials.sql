-- +goose Up
-- +goose StatementBegin

-- RUN-247 / docs/34 §6: WebAuthn passkey credentials. Nothing in a row is
-- secret (public keys, IDs, counters/flags — docs/34 §3.6), so no DB-key
-- encryption applies. credential_id is globally UNIQUE from day one: the
-- credential-ID → user lookup IS the identity mechanism for passwordless
-- login (docs/34 §3.2), so it is multi-user-safe before RUN-236 exists.
-- user_id carries the FK from day one for the same reason. No challenges
-- table: ceremony state is in-memory only (docs/34 §3.4).
CREATE TABLE webauthn_credentials (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL,
    name TEXT NOT NULL DEFAULT 'Passkey',           -- user-chosen label
    credential_id BLOB NOT NULL UNIQUE,             -- raw credential ID bytes
    public_key BLOB NOT NULL,                       -- COSE public key (library canonical form)
    aaguid TEXT NOT NULL DEFAULT '',                -- recorded, never enforced
    attestation_type TEXT NOT NULL DEFAULT '',
    transports TEXT NOT NULL DEFAULT '',            -- CSV: usb,nfc,ble,internal,hybrid
    sign_count INTEGER NOT NULL DEFAULT 0,
    backup_eligible INTEGER NOT NULL DEFAULT 0,
    backup_state INTEGER NOT NULL DEFAULT 0,
    clone_warning INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_used_at DATETIME NOT NULL DEFAULT '1970-01-01 00:00:00',
    FOREIGN KEY(user_id) REFERENCES admin_users(id) ON DELETE CASCADE
);

CREATE INDEX idx_webauthn_credentials_user ON webauthn_credentials(user_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE webauthn_credentials;
-- +goose StatementEnd
