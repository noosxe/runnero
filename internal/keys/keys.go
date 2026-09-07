package keys

import (
	"crypto/hkdf"
	"crypto/sha256"
	"fmt"
)

// Context labels for HKDF expansion (the RFC 5869 "info" parameter). Each
// secret gets a distinct, self-describing label so the two outputs are
// domain-separated: the same master key under different labels always
// produces unrelated keys, and no other label in this package may ever
// collide with these. The /v1 suffix leaves room for a future rotation —
// a v2 label derives fresh secrets without disturbing v1-derived ones
// during a migration.
const (
	LabelDBEncryption = "runnero/supervisor/db-encryption/aes-256/v1"
	LabelJWTSigning   = "runnero/supervisor/jwt-signing/hmac-sha256/v1"
)

// Sizes of the derived secrets and the floor on their shared master key.
const (
	// DBEncryptionKeySize is 32 bytes: a full AES-256 key for encrypting
	// sensitive values at rest in SQLite (docs/05 §5).
	DBEncryptionKeySize = 32
	// JWTSigningKeySize is 32 bytes: a 256-bit key for HMAC-SHA-256
	// session-token signing, matching the hash's output size for full
	// strength.
	JWTSigningKeySize = 32
	// MinMasterKeyBytes mirrors config.MinEncryptionKeyBytes: the master
	// key seeds every runtime secret, so it must carry at least 256 bits
	// of key material. It is re-checked here (defense in depth) so the
	// package stays safe even if a caller bypasses configuration
	// validation.
	MinMasterKeyBytes = 32
)

// Derived holds the two runtime secrets derived from the master key. The
// fields are independent: each is a separate HKDF output of the master key
// under its own context label, so neither can be computed from the other.
type Derived struct {
	// DBEncryptionKey is the AES-256 key for encrypting credentials stored
	// in the database (consumed by internal/db, RUN-12..RUN-18).
	DBEncryptionKey []byte
	// JWTSigningSecret signs admin session tokens (consumed by
	// internal/server). No separate JWT_SECRET env var exists (OQ #3).
	JWTSigningSecret []byte
}

// Derive expands the configured master key (SUPERVISOR_DB_ENCRYPTION_KEY,
// already validated by internal/config) into the runtime secrets held by
// Derived.
//
// The raw bytes of the configured value are used directly as HKDF input
// keying material. Whether the operator configured base64 text or raw
// binary, HKDF-Extract normalizes it into a pseudorandom key, and the
// fixed rule — "raw bytes of exactly what was configured" — is all
// determinism requires: the same configured value must always derive the
// same secrets so existing ciphertexts and issued JWTs stay valid across
// restarts. The salt is nil (RFC 5869 permits an absent salt; domain
// separation comes from the context labels). Derived secrets and the
// master key are never logged.
func Derive(masterKey string) (*Derived, error) {
	ikm := []byte(masterKey)
	if n := len(ikm); n < MinMasterKeyBytes {
		return nil, fmt.Errorf("master key is %d bytes, want at least %d: refusing to derive runtime secrets from weak input (set SUPERVISOR_DB_ENCRYPTION_KEY, e.g. openssl rand -base64 32)", n, MinMasterKeyBytes)
	}

	dbKey, err := derive(ikm, LabelDBEncryption, DBEncryptionKeySize)
	if err != nil {
		return nil, fmt.Errorf("deriving database encryption key: %w", err)
	}
	jwtSecret, err := derive(ikm, LabelJWTSigning, JWTSigningKeySize)
	if err != nil {
		return nil, fmt.Errorf("deriving JWT signing secret: %w", err)
	}

	logger.Debug("runtime secrets derived from master key",
		"db_encryption_key_bytes", len(dbKey),
		"jwt_signing_secret_bytes", len(jwtSecret),
	)
	return &Derived{DBEncryptionKey: dbKey, JWTSigningSecret: jwtSecret}, nil
}

// derive performs the RFC 5869 extract-then-expand for one label. Both
// secrets share this single code path, differing only in label and size.
func derive(ikm []byte, label string, size int) ([]byte, error) {
	return hkdf.Key(sha256.New, ikm, nil, label, size)
}
