// Package keys derives the supervisor's runtime secrets from the single
// master key configured through SUPERVISOR_DB_ENCRYPTION_KEY (RUN-9).
//
// One master key in, one independent secret out today: HKDF (RFC 5869,
// SHA-256) expands the configured master key into the AES-256 database
// encryption key under its own context label. The label keeps the output
// domain-separated — future secrets derived for other purposes (each under
// a fresh label) will be independent of it — while the deterministic
// derivation guarantees that the same configured master key yields the
// same secret on every boot, so encrypted database rows stay readable
// across process restarts and binary upgrades.
//
// Session authentication no longer derives a signing secret: since the
// opaque-session redesign (RUN-230, docs/32 §2.1) session tokens are
// 32-byte crypto/rand values validated by SHA-256 hash lookup against the
// sessions table — nothing to sign, no secret to keep (the former
// HMAC/JWT secret is retired; docs/05-security-and-isolation.md §5).
package keys
