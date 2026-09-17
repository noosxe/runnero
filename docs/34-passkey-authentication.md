# 34 — Passkey (WebAuthn) Login for the Web Control Plane

> Status: **design phase, revision 2** (RUN-235). Implementation is sequenced
> after this doc merges; the implementation PR(s) carry code, tests, and the
> README Features listing. Revision 2 (owner decision, pre-merge): passkey
> sign-in is **passwordless** — the credential identifies the user
> completely; username/password remains as an independent fallback path. Tracker: docs/32 §2.4 deferred this module and
> schema-accommodated it ("audit vocabulary leaves room for passkey events",
> `GetOnboardingStatus` is the pre-auth status surface a passkey flag
> extends).

## 1. Problem

Today the web control plane authenticates with exactly one factor: the admin
password (docs/32). That password is the single root of trust for a system
whose runners execute untrusted workflow code. The realistic threats the
password alone does not cover:

- **Credential reuse / stuffing** — the admin reuses a password elsewhere; a
  breach there becomes a Runnero compromise. Rate limiting (docs/32 §4.2)
  slows online guessing but cannot see offline reuse.
- **Phishing** — a convincing proxy page harvests the password in transit.
  Sliding-session cookies (docs/32 §3.3) mean one harvest buys a long-lived
  session.
- **Shoulder-surf / infostealer password exfiltration** — browser password
  stores are a standing target; a passkey private key never leaves the
  authenticator (or is hardware-bound), so it cannot be exfiltrated the same
  way.

WebAuthn/passkeys answer all three: the login ceremony is bound to the RP ID
and origin (phishing breaks it), produces a per-site key pair (reuse is
meaningless), and the secret never leaves the authenticator.

## 2. Goals and non-goals

**Goals**

1. Passkey sign-in as a **complete passwordless login**: a "Sign in with
   passkey" button on the login screen identifies the user entirely via the
   credential — no password on that path — with username/password remaining
   alongside as an independent fallback.
2. Phishing resistance end to end: RP ID/origin pinning, server-controlled
   challenges, user verification (biometric/PIN) required on every ceremony.
3. Fail-closed configuration: passkeys are inert until an RP ID is explicitly
   configured (same "empty config → strict" posture as the rest of the
   supervisor).
4. Multi-user-ready storage: credentials carry a `user_id` FK from day one
   (RUN-236 interaction — the schema never needs to move again).
5. Recovery without a second channel: the two login paths are independent —
   a lost passkey degrades to username/password, a forgotten password leaves
   the passkey; total lockout requires losing both.

**Non-goals (this design)**

- **Password-disabled lockdown mode** (passkey-only). The password path
  stays enabled in this design — it is half of the recovery story (§3.2); a
  config switch to disable it is a deliberate future decision (§13).
- **External IdPs / OIDC, TOTP** — untouched (docs/32 non-goals stand).
- **Conditional UI / autofill mediation** — the v1 ceremony uses the
  standard authenticator prompt; browser autofill-style mediation is a UI
  enhancement layered on the same RPCs later.
- **Attestation verification** (MDS lookup, AAGUID allow-lists) — attestation
  is recorded, never *enforced*; consumer passkeys are routinely
  "none"-attested and sync-based, so an allow-list would lock out exactly the
  credentials people actually have.
- **Per-runner-container WebAuthn** — the forge-facing runner side is out of
  scope; this is control-plane UI auth only.

## 3. Architecture decisions

### 3.1 Library: `go-webauthn/webauthn` (verified against v0.18.1)

The de-facto standard Go server library (WebAuthn Level 3). The API surface
this design leans on, verified against upstream docs at v0.18.1:

- `webauthn.Config{RPID, RPDisplayName, RPOrigins}` + `webauthn.New` —
  Relying Party definition;
- `User` interface (`WebAuthnID/Name/DisplayName/Credentials`) — implemented
  by an adapter over the admin user + its credential rows;
- `BeginRegistration(user, WithResidentKeyRequirement(Required),
  WithUserVerification(Required))` → `*protocol.CredentialCreation,
  *SessionData`;
- `BeginDiscoverableLogin(opts...)` → `*protocol.CredentialAssertion,
  *SessionData` — the **discoverable** (passkey) assertion: no username, no
  allow-list; the credential identifies the user;
- `FinishPasskeyLogin(handler, session, request)` → `(User, *Credential)`
  — the handler resolves the user from the asserted credential ID; the
  returned credential carries the updated `Authenticator.SignCount` and
  `Flags`, which the library requires the caller to write back (§3.8);
- `protocol.ErrChallengeMismatch`, `ErrAssertionSignature`,
  `ErrBadRequest`, `ErrorUnknownCredential` — the error taxonomy the server
  maps onto rate-limiter feeds and audit events.

The library's canonical Finish steps take `*http.Request`; Runnero's handlers
are ConnectRPC. The boundary therefore parses the assertion/attestation JSON
from the proto payload with the library's own parsers and calls the parsed-
response entry points (`CreateCredential`, `ValidatePasskeyLogin`) — a thin,
easily unit-tested adapter (§9).

### 3.2 Passwordless passkey sign-in (revision 2)

Revision 1 of this design framed the passkey as a second factor chained
after the password. The owner revised the direction pre-merge: **the
passkey identifies the user completely** — a "Sign in with passkey" button
on the login screen is a full login on its own, and username/password
remains alongside as an independent fallback path. What the revision buys:

- **Simpler, faster primary login** — one ceremony, no password entry; the
  discoverable credential *is* the username.
- **No phishing surface on the primary path** — the ceremony is bound to
  the RP ID/origin; a proxy page cannot relay it, and nothing reusable is
  transmitted.
- **Recovery by parallelism, not chaining** — a lost passkey degrades to
  the password path; a forgotten password leaves the passkey. Total lockout
  requires losing both.

Accepted trade-off (documented, not hidden): possession of the passkey is
full account access. A synced passkey is only as strong as its sync account
(iCloud Keychain / Google Password Manager); the mitigations are user
verification on every ceremony (§3.7) and sign-counter clone detection
(§3.8), and device-bound roaming keys (YubiKey) remain available for owners
who want hardware residency. A future "disable password login" lockdown
mode is deliberately out of scope (§13).

Mechanically this is the library's discoverable ceremony, verified
upstream at v0.18.1: `BeginDiscoverableLogin()` →
`FinishPasskeyLogin(handler, session, request)`, where the handler resolves
the `User` from the asserted credential ID.
`webauthn_credentials.credential_id` is globally `UNIQUE` from day one, so
credential-ID → user lookup is multi-user-safe with zero schema motion
(RUN-236 interaction).

### 3.3 Login flows: two independent complete paths

```
passkey path (WebAuthn configured):
  BeginPasskeyLogin()            → CredentialAssertion options (no username,
                                   empty allow-list — discoverable ceremony)
  user picks credential + UV     → authenticator signs the challenge
  FinishPasskeyLogin(assertion)  → credential_id → user → session cookie
                                                          (shared issuance path)

password path (always available, unchanged):
  Login(username, password)      → session cookie      (docs/32 hardened path)
```

- The two paths share **nothing**: no ceremony state, no tickets, no
  chaining. Revision 1's assertion-ticket machinery is gone entirely —
  passwordless has no password step to bind to, so there is nothing to
  chain from.
- Ceremony state still never leaves the server (§3.4), challenges are
  single-use with a 3-minute TTL, and `FinishPasskeyLogin` issues the
  session through the **one shared issuance path** (docs/32 §3.2 invariant
  preserved: session rows, two clocks, cookie attributes identical to a
  password login; audit `auth.login_success` records `method: "passkey"`).
- The passkey path's Begin/Finish run **without any credential** (§4.3
  bounds the anonymous surface this creates).

### 3.4 Ceremony state never leaves the server

`go-webauthn`'s `SessionData` (challenge, allowed credential IDs, expiry,
user-verification requirement) is documented as needing integrity-protected,
atomic server-side storage. The obvious REST-era pattern — round-trip it
through the client as a signed blob — is rejected: it puts the challenge in
attacker-reachable storage and makes challenge substitution a client-side
question. Instead:

- all ceremony state lives in an **in-memory store** on the supervisor,
  keyed by a random ceremony ID, value = `SessionData`, consumed on first
  use (single-use), TTL 3 minutes, bounded (one live enrollment ceremony
  per user; a capped pool of anonymous login ceremonies, §4.3;
  oldest-expired evicted);
- the client sees only the public options JSON — challenge bytes never
  cross the wire in either direction. A supervisor restart cancels
  in-flight ceremonies — visible as a failed step the UI offers to
  restart.

### 3.5 RP ID / origins: explicit config, fail-closed

WebAuthn binds credentials to a **Relying Party ID** (an registrable domain)
and validates the ceremony origin against it. For a self-hosted tool this is
the sharpest operational edge — accessing the supervisor via
`http://100.x.y.z:8090` (a Tailscale IP) and via `https://runnero.tailnet-xyz.ts.net`
are different browser *origins*, and a passkey enrolled under one RP ID will
not assert on another. Browsers additionally refuse WebAuthn on raw-IP
origins (localhost excepted).

Decisions:

- new config keys `webauthn_rp_id` (string, default **empty**) and
  `webauthn_origins` (CSV of allowed origins, e.g.
  `https://runnero.tailnet-xyz.ts.net`);
- **empty `webauthn_rp_id` ⇒ the entire feature is off**: no enrollment UI,
  no passkey button on the login page, passkey RPCs answer
  `FailedPrecondition`, and `Login` behaves exactly as today. Fail-closed,
  nothing half-configured;
- `webauthn_origins` defaults to `https://<rp_id>` (port 443 implicit) and
  must be set explicitly for non-443 ports or extra origins; startup
  validation rejects origins whose host is not the RP ID or a subdomain of
  it (the WebAuthn effective-domain rule) and rejects scheme-less or
  wildcard entries;
- `RPDisplayName` = `Runnero` (static);
- rollout note goes in the PR upgrade section: users should access the
  supervisor through a **stable hostname** (Tailscale MagicDNS name, or a
  reverse-proxy domain) before enrolling; enrolling over an IP origin will be
  refused at `webauthn.New` validation with a pointer to the docs;
- `localhost`/`127.0.0.1` origins are accepted (browsers special-case them)
  — this keeps the local/E2E flows workable.

### 3.6 Credential storage: public keys are not secrets

The Linear issue anticipated "credential storage/encryption via the existing
DB key". On inspection **nothing in a WebAuthn credential is secret**: the
server holds only the *public* key, the credential ID (a public identifier),
and counters/flags. Encrypting them with the DB key would add a decryption
dependency to the auth path for zero threat reduction — an attacker with the
DB cannot authenticate with a public key. Decision:

- **no new key derivation label, no encryption of credential rows**; the
  master key keeps deriving the DB encryption key only (docs/32 §6 end
  state unchanged);
- what makes the system safe is that the private key lives in the
  authenticator — the design documents this explicitly so a future
  contributor does not "helpfully" add encryption and a key label.

### 3.7 Registration options locked to passkey-grade credentials

`BeginRegistration` is called with:

- `WithResidentKeyRequirement(Required)` — credentials MUST be
  discoverable: discoverability is the login mechanism in passwordless mode
  (the authenticator lists "Runnero" and identifies the account);
  device-bound security keys get the same UX via the allow-list-free
  discoverable ceremony;
- `WithUserVerification(Required)` — biometric/PIN on every ceremony, both
  registration and assertion; the UV flag is checked server-side on the
  returned credential/flags, and an assertion without UV is rejected. In
  passwordless mode UV stands where the password used to — it is
  non-negotiable on this path;
- attestation `none` (default): no attestation conveyance, nothing to
  verify, maximum authenticator compatibility (§2 non-goal).
- Attachments are **not** restricted: platform (Touch ID / Windows Hello /
  Android) *and* roaming (YubiKey) authenticators both fine; the backup
  flags recorded at registration/assertion time are surfaced in the UI as
  "synced" vs "device-bound" (informational — §7).

### 3.8 Sign-count policy (cloned-authenticator detection)

The library returns the authenticator's counter with every assertion and
requires the new value + flags to be written back. Policy:

- stored `sign_count == 0` and incoming `== 0`: counterless authenticator
  (common for synced passkeys) — skip comparison, update nothing;
- otherwise the counter MUST be strictly greater than the stored value. A
  non-increasing non-zero counter ⇒ probable credential clone: the assertion
  is **rejected (fail closed)**, the row is flagged `clone_warning = true`,
  and audit records `auth.passkey.clone_warning`. A flagged credential
  refuses all further assertions until an authenticated admin removes it
  (the flag is visible in the UI list as a red banner).
- `BackupState` transitions (synced→restored on a new device) are recorded,
  not enforced.

### 3.9 Roles and buckets

New procedures in the bucket matrix (docs/32 §3.3 vocabulary):

| procedure | bucket | gate |
|---|---|---|
| `BeginPasskeyEnrollment`, `FinishPasskeyEnrollment` | `bucketSession` | admin role + session; Begin additionally verifies the **current password** (§4.2) |
| `ListPasskeys`, `RenamePasskey`, `DeletePasskey` | `bucketSession` | admin role + session |
| `BeginPasskeyLogin`, `FinishPasskeyLogin` | `bucketPublic` | fully anonymous: bounded ceremony store + IP rate limit (§3.3, §4.3) |
| `Login` (extended response) | `bucketPublic` | unchanged |

A RUN-244-style degrade does not apply: these `bucketSession` procedures are
session-or-401 like the rest of the admin surface.

## 4. Login lifecycle

### 4.1 Two independent paths

The login screen offers both; the server treats them as unrelated complete
logins:

- **Passkey (passwordless, primary):** `BeginPasskeyLogin` — no username, no
  credential — returns assertion options with an empty allow-list; the
  authenticator prompts for user verification and shows the user their
  registered credentials; the assertion's credential ID resolves the user
  server-side; `FinishPasskeyLogin` issues the session through the shared
  issuance path. With a single admin this always resolves to the admin;
  with N users (RUN-236) it resolves to whoever holds the key — the
  credential-ID lookup *is* the identity mechanism.
- **Username/password (fallback):** the docs/32 `Login` RPC, untouched —
  same rate limiting, timing equalization, session issuance, response
  shape. A user with passkeys enrolled may still use it; nothing chains.

`GetOnboardingStatus.passkey_available` (§7) tells the pre-auth login page
whether to render the passkey button; the passkey RPCs answer
`FailedPrecondition` when invoked while unconfigured — fail-closed even if
a cached flag is stale (RUN-243's guard contract keeps applying to the
login surface).

### 4.2 Enrollment starts with a password re-check

`BeginPasskeyEnrollment` requires the **current password** in its request and
verifies it with one bcrypt comparison before minting the ceremony. Threat:
a stolen-but-live session (cookie exfiltrated from a sleeping laptop,
malicious extension) must not be able to mint a *new complete login
identity*. The session alone is proof of "was authenticated"; the password
re-check restores "is authenticated" for an identity-granting action. The
same posture as `ChangePassword` (docs/32 §4.4) — and more important under
passwordless, where the minted factor is a full login.

### 4.3 Anonymous surface, bounded and limited

`BeginPasskeyLogin` runs with no credential at all, so the design keeps its
unauthenticated footprint fixed:

- the anonymous ceremony store caps concurrent ceremonies (32), TTL 3
  minutes, oldest-expired evicted; a full store rejects Begin with
  `ResourceExhausted` — fail-closed, no queue to fill;
- Begin calls rate-limit per client IP (fixed pseudo-user key in the
  existing durable limiter);
- Finish failures rate-limit per client IP, upgrading to the standard
  `username + IP` key once the assertion identifies the user (docs/32 §4.2
  reservation honored);
- all store bounds are in-memory by design — a restart drains them, which
  is harmless: the client restarts the ceremony (§3.4).

### 4.4 Session control parity

Passkey-issued sessions are ordinary sessions: they appear in the Security
tab list, are revocable, ride the same two clocks, and `ChangePassword`'s
revoke-others sweep kills them like any other (docs/32 §3.5/§4.4). Removing
a credential does **not** revoke sessions issued earlier through it — session
revocation is the lever for that (stated explicitly in the UI copy for
removal).

## 5. Hard invariants

1. **Two independent complete paths:** the passkey path and the password
   path are each a full login on their own; no ceremony state, ticket, or
   extra step ever chains one into the other, and the password path's
   behavior is byte-for-byte docs/32.
2. **One challenge, one use:** every challenge is single-use, server-generated,
   3-minute TTL; replay or reuse is a hard failure and a limiter event.
3. **Server-side ceremony state only:** challenge bytes and SessionData never
   cross the wire in either direction (leakage-test asserted, §10).
4. **UV required:** assertions with `flags.UV=false` are rejected as if the
   signature were bad (same audit action).
5. **Audit carries no credential material:** no credential IDs, no challenge
   bytes, no attestation objects — extend the existing leakage test.
6. **Bounded anonymous state:** `BeginPasskeyLogin` runs without any
   credential; the ceremony store caps concurrency and IP rate limits Begin,
   so the unauthenticated surface grows state only up to a fixed bound
   (§4.3).

## 6. Data model (migration `012_webauthn_credentials.sql`)

```sql
-- +goose Up
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
-- +goose Down
DROP TABLE webauthn_credentials;
```

- `user_id` FK from day one: RUN-236's multi-user work adds users, not
  columns.
- No challenges table — ceremony state is in-memory by design (§3.4); the
  hourly sweeper (docs/32 §5.2) needs no change.

## 7. Protocol changes (`proto/api.proto`)

Wire serde: the library's options and the browser's responses are JSON
shapes (`protocol.CredentialCreation`, `protocol.CredentialAssertion`,
`PublicKeyCredentialJSON`). Rather than hand-transcribing W3C structures
into proto (a maintenance and drift hazard), the RPCs carry them as
`bytes` holding canonical JSON, validated at the boundary by the library's
own parsers. The proto remains a transport, not a second schema.

```proto
// AuthService additions
rpc BeginPasskeyEnrollment(BeginPasskeyEnrollmentRequest)
    returns (BeginPasskeyEnrollmentResponse);
rpc FinishPasskeyEnrollment(FinishPasskeyEnrollmentRequest)
    returns (FinishPasskeyEnrollmentResponse);
rpc ListPasskeys(ListPasskeysRequest) returns (ListPasskeysResponse);
rpc RenamePasskey(RenamePasskeyRequest) returns (RenamePasskeyResponse);
rpc DeletePasskey(DeletePasskeyRequest) returns (DeletePasskeyResponse);
rpc BeginPasskeyLogin(BeginPasskeyLoginRequest)
    returns (BeginPasskeyLoginResponse);
rpc FinishPasskeyLogin(FinishPasskeyLoginRequest)
    returns (LoginResponse);          // full login: identifies the user via the
                                      // credential, issues the standard session

message BeginPasskeyEnrollmentRequest { string current_password = 1; }
message BeginPasskeyEnrollmentResponse {
  bytes public_key_options_json = 1;   // protocol.CredentialCreation JSON
}
message FinishPasskeyEnrollmentRequest {
  string ceremony_name = 1;            // reserved; v1 has a single live ceremony
  bytes attestation_response_json = 2; // PublicKeyCredentialJSON (registration)
  string name = 3;                     // user label
}
message FinishPasskeyEnrollmentResponse { PasskeyInfo passkey = 1; }

message PasskeyInfo {
  uint64 id = 1;
  string name = 2;
  google.protobuf.Timestamp created_at = 3;
  google.protobuf.Timestamp last_used_at = 4;
  bool backup_eligible = 5;   // "synced" vs "device-bound" chip
  bool backup_state = 6;
  bool clone_warning = 7;     // red banner when true
}

message ListPasskeysRequest {}
message ListPasskeysResponse { repeated PasskeyInfo passkeys = 1; }
message RenamePasskeyRequest { uint64 id = 1; string name = 2; }
message RenamePasskeyResponse {}
message DeletePasskeyRequest { uint64 id = 1; }
message DeletePasskeyResponse {}

message BeginPasskeyLoginRequest {}
message BeginPasskeyLoginResponse {
  bytes public_key_options_json = 1;   // protocol.CredentialAssertion JSON;
                                       // empty allow-list (discoverable ceremony)
}
message FinishPasskeyLoginRequest {
  bytes assertion_response_json = 1;   // PublicKeyCredentialJSON (authentication)
}
```

`LoginResponse` itself is unchanged — the passkey path reuses it as
`FinishPasskeyLogin`'s return type, issuing the identical session shape
through the shared issuance path (§3.3).

`GetOnboardingStatusResponse` gains `bool passkey_available` (true iff
WebAuthn is configured) — the pre-auth status field docs/32 §2.4 reserved;
the login page reads it to decide whether to render the passkey button.
Availability is a property of configuration, not enrollment state, so it
never flickers when credentials are added or removed.

## 8. Audit

New actions on the existing `audit_logs` (vocabulary docs/32 §5.1 extended
exactly as reserved):

| action | when | details |
|---|---|---|
| `auth.passkey.enrolled` | Finish success | label, transports, backup flags |
| `auth.passkey.enroll_failed` | Finish failure | coarse reason (`bad_attestation`, `password_mismatch`, `ceremony_expired`) |
| `auth.passkey.removed` | DeletePasskey | label |
| `auth.passkey.renamed` | RenamePasskey | — |
| `auth.passkey.assertion_failed` | Finish failure | coarse reason (`bad_signature`, `uv_missing`, `ceremony_invalid`, `counter_regression`) |
| `auth.passkey.clone_warning` | counter regression | credential row id |
| `auth.login_success` | (existing) | gains `method: "password" \| "passkey"` |

Rate-limited assertion attempts keep `auth.rate_limited` with the new
`method: "passkey"` detail. The leakage test extends: audit rows must not
contain credential IDs, challenge bytes, or attestation objects.

## 9. Server layout

One self-contained module, mirroring the docs/32 §2.4 prediction:

- `internal/server/passkey.go` — RPC handlers; ceremony store (enrollment +
  capped anonymous-login pool); the JSON-in-bytes parse adapter feeding the
  library's parsed-response Finish entry points;
- `internal/server/passkey_test.go` — full ceremony tests using the library
  against a software authenticator (the `descope/virtualwebauthn` helper
  package, dev-only dependency) so registration/assertion crypto is
  exercised for real, not mocked;
- `internal/keys` — **unchanged** (§3.6);
- `internal/db` — queries + migration 012;
- `internal/config` — `webauthn_rp_id`, `webauthn_origins` + startup
  validation.

Config additions:

| Key (env `SUPERVISOR_*`, flag) | Default | Notes |
|---|---|---|
| `webauthn_rp_id` | *(empty)* | empty ⇒ feature fully off |
| `webauthn_origins` | `https://<rp_id>` | CSV; validated against RP ID at startup |

There is no "require passkey" switch: the password path is half of the
recovery story (§3.2), so it stays enabled by design. The Security tab
instead recommends enrolling a second passkey on a different device/sync
account.

## 10. Security implications (threat walk)

| threat | mitigation |
|---|---|
| Password phishing/harvest | unchanged exposure on the password path vs today (docs/32 hardening); the passkey button is the phishing-resistant path — origin/RP-bound, nothing reusable transmitted (§3.2) |
| Synced-passkey cloud compromise | possession = full access — owner-accepted trade-off of passwordless (§3.2); UV on every ceremony + clone detection (§3.8); device-bound keys available for hardware residency |
| Stolen session adds attacker passkey (a complete login) | enrollment Begin requires the current password (§4.2) — the primary defense under passwordless |
| Challenge substitution / ceremony mix-up | SessionData server-side, single-use, per-user isolation (§3.4, §5.3) |
| Assertion replay | challenge single-use + TTL + limiter feed (§5.2) |
| Cloned authenticator | sign-counter monotonic policy, fail closed + flag (§3.8) |
| UV-less assertion (e.g. fingerprint bypassed) | UV flag enforced server-side (§5.4) |
| Anonymous ceremony exhaustion | `BeginPasskeyLogin` is credential-free: bounded store (cap 32, 3-min TTL, oldest eviction) + IP rate limit keep state growth fixed (§5.6) |
| DB theft | public keys only — useless without the authenticator (§3.6) |
| Online brute force of assertions | signature forgery is the only path in; challenges are single-use with 3-min TTL and failures rate-limit per IP (§4.3) |
| CSRF on ceremony RPCs | Connect content-type requirement + `SameSite=Strict` cookie (docs/32 §2.2); login ceremonies are keyed by server-minted challenges that never cross the wire outbound (§3.4) |
| DoS via enrollment ceremonies | Begin is session- + password-gated; one live ceremony per user; TTL eviction |
| Lockout | two independent paths: losing the passkey leaves the password, forgetting the password leaves the passkey — total lockout requires losing both (§3.2) |

## 11. Testing & verification

- **Go unit** (`internal/server/passkey_test.go`): full enroll+assert
  ceremonies via the virtual authenticator; discoverable assertion
  resolving the user by credential ID (single-admin and
  unknown-credential cases); password fallback succeeding with passkeys
  enrolled; anonymous Begin bounds (cap, TTL eviction, single-use); IP
  rate-limit feeds; UV-missing rejection; counter
  regression → clone warning + rejection; unconfigured ⇒ endpoints are
  `FailedPrecondition`, `Login` unchanged; origin validation at startup;
  leakage test extension (§8).
- **Web unit** (Vitest): login screen passkey button states
  (`passkey_available` gating, error mapping of `NotAllowedError` to a
  retryable message); Security-tab
  passkey list rendering incl. sync/clone chips; enrollment flow happy path
  with a mocked ceremony client.
- **E2E** (Playwright, Chromium): CDP `WebAuthn.enable` + virtual
  authenticator — enroll on the Security tab, log out, passwordless login
  with the virtual passkey, password-fallback login, wrong-key rejection,
  `GetOnboardingStatus` flag flips. Runs with `webauthn_rp_id=localhost` in
  the E2E compose env (the
  §3.5 localhost carve-out makes this self-contained).
- **Gates**: full chain per AGENTS.md before push.

## 12. Implementation phasing (Linear issues after doc merge)

1. **Backend** — migration 012, config + validation, ceremony/ticket stores,
   proto + handlers, audit, Go tests (virtual authenticator). Feature ships
   dark (no UI) but wire-complete behind the config flag.
2. **Frontend + E2E** — login passkey button (passwordless) + password
   fallback, Security-tab passkey management,
   Vitest, Playwright CDP flows, `GetOnboardingStatus` flag consumption.

Split keeps each PR reviewable; neither is human-testable meaningfully
without the other, so both land before the owner does passkey manual checks.

## 13. Open questions (owner)

1. **Passkey enrollment during onboarding** — offer it at the end of the SetupAdmin flow (nice first-run UX for passwordless), or keep it Settings-only for v1?
2. **RP ID guidance** — the doc recommends a stable MagicDNS/reverse-proxy hostname. OK to make the E2E + docs assume `localhost` for dev and a hostname for prod, with no IP-origin support at all?
3. **Multiple passkeys** — design allows N per user (recommended: enroll 2 on different devices). Cap at, say, 8, or uncapped?
4. **Sequencing vs RUN-236 (viewer role)** — schema is multi-user-ready; if viewer lands first, passkey scoping is already correct. Preference on which PR series goes first?
