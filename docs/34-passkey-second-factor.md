# 34 — Passkey (WebAuthn) Second Factor for the Web Control Plane

> Status: **design phase** (RUN-235). Implementation is sequenced after this
> doc merges; the implementation PR(s) carry code, tests, and the README
> Features listing. Tracker: docs/32 §2.4 deferred this module and
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

1. Passkeys as an **opt-in second factor** on top of the password: enroll in
   Settings → Security, then every login is *password → passkey assertion*.
2. Phishing resistance end to end: RP ID/origin pinning, server-controlled
   challenges, user verification (biometric/PIN) required on every ceremony.
3. Fail-closed configuration: passkeys are inert until an RP ID is explicitly
   configured (same "empty config → strict" posture as the rest of the
   supervisor).
4. Multi-user-ready storage: credentials carry a `user_id` FK from day one
   (RUN-236 interaction — the schema never needs to move again).
5. Recovery without a second channel: the password path remains a complete
   login on its own when no passkey is enrolled or the configured mode does
   not demand one, so a lost passkey is an inconvenience, not a lockout.

**Non-goals (this design)**

- **Passwordless login** (passkey as a *replacement* first factor). Rejected
  for now — see §3.2; the credential schema does not preclude it later.
- **External IdPs / OIDC, TOTP** — untouched (docs/32 non-goals stand).
- **Conditional UI / username-less autofill ceremonies** — needs the
  passwordless mode above to be meaningful.
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
- `BeginLogin(user, WithAllowedCredentials(descriptors))` →
  `*protocol.CredentialAssertion, *SessionData` — the **non-discoverable**
  assertion, correct for a second factor because the password step has
  already identified the user;
- `FinishLogin(user, session, request)` → `*Credential` with the updated
  `Authenticator.SignCount` and `Flags` — the library requires the caller to
  write these back (§3.6);
- `protocol.ErrChallengeMismatch`, `ErrAssertionSignature`,
  `ErrBadRequest`, `ErrorUnknownCredential` — the error taxonomy the server
  maps onto rate-limiter feeds and audit events.

The library takes `*http.Request` at the Finish steps; Runnero's handlers are
ConnectRPC. The design therefore extracts the assertion/attestation JSON from
the proto payload and synthesizes a minimal `*http.Request` carrying it as
the body — a thin, easily unit-tested adapter (§9).

### 3.2 Second factor, not passwordless

The Linear issue frames passkeys as a *second* authentication factor, and
that is the right call for a self-hosted single-admin product:

- **Recovery.** Passwordless means a lost/broken authenticator is a total
  lockout with no second channel (there is no "email me a reset link" in a
  self-hosted box). With the password as the always-present first factor, a
  lost passkey degrades to today's login; the owner re-enrolls after signing
  in.
- **Threat fit.** The password remains the knowledge factor; the passkey adds
  the possession factor. Every threat in §1 is answered by the *combination*.
- **Downgrade resistance is what makes 2FA real** — §5.2's invariant, not
  user discipline.

Passwordless stays a documented rejection with a named unlock condition: if
the owner later wants it, the `ResidentKey=Required` credentials enrolled
under this design are already discoverable — the change is a ceremony and UI
change, not a re-enrollment.

### 3.3 Login becomes two-phase when a passkey is enrolled

```
no passkeys enrolled (or passkeys disabled):
  Login(password) ──► session cookie                      (unchanged path)

≥1 passkey enrolled:
  Login(password) ──► 200 { passkey_required: true,       NO session issued
                            assertion_ticket: "<256-bit random>" }
  BeginPasskeyAssertion(ticket)  ──► CredentialAssertion options (JSON)
  FinishPasskeyAssertion(ticket, authenticatorData) ──► session cookie
                                                          (shared issuance path)
```

- The **assertion ticket** is issued only inside the existing hardened login
  response (rate limit, timing equalization — docs/32 §4 all apply to the
  password step unchanged). It is: 256 bits of CSPRNG, stored in memory only,
  single-use, bound to `user_id + client IP`, TTL **3 minutes**. A supervisor
  restart discards outstanding tickets — the client just re-enters the
  password; no durability is needed because the first factor is cheap to
  repeat.
- The ticket — not the network — is what gates `BeginPasskeyAssertion` /
  `FinishPasskeyAssertion`. The unauthenticated surface does not grow: an
  anonymous caller cannot mint challenges or write ceremony state, and there
  is no new table to purge.
- `FinishPasskeyAssertion` issues the session through the **one shared
  issuance path** (docs/32 §3.2 invariant preserved verbatim: session rows,
  two clocks, cookie attributes identical to a password login).

### 3.4 Ceremony state never leaves the server

`go-webauthn`'s `SessionData` (challenge, allowed credential IDs, expiry,
user-verification requirement) is documented as needing integrity-protected,
atomic server-side storage. The obvious REST-era pattern — round-trip it
through the client as a signed blob — is rejected: it puts the challenge in
attacker-reachable storage and makes challenge substitution a client-side
question. Instead:

- all ceremony state lives in an **in-memory store** on the supervisor,
  keyed by a random ceremony ID, value = `SessionData`, consumed on first
  use (single-use), TTL 3 minutes, bounded (one live ceremony per user for
  enrollment; one per ticket for login; oldest-expired evicted);
- the client sees only opaque IDs (ticket) and the public options JSON.
  A supervisor restart cancels in-flight ceremonies — visible as a failed
  step the UI offers to restart.

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
  no ticket issuance, `Login` behaves exactly as today. Fail-closed, nothing
  half-configured;
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

- `WithResidentKeyRequirement(Required)` — credentials are discoverable
  (device-bound security keys synthesized RoRIs; sync passkeys are native
  discoverable). This is the future-proofing for §3.2;
- `WithUserVerification(Required)` — biometric/PIN on every ceremony, both
  registration and assertion; the UV flag is checked server-side on the
  returned credential/flags, and an assertion without UV is rejected —
  "something you have" must also prove "something you are/know" locally;
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
| `BeginPasskeyAssertion`, `FinishPasskeyAssertion` | `bucketPublic` | gated by the in-memory ticket, not the cookie |
| `Login` (extended response) | `bucketPublic` | unchanged |

A RUN-244-style degrade does not apply: these `bucketSession` procedures are
session-or-401 like the rest of the admin surface.

## 4. Login lifecycle

### 4.1 Flow selection

`Login` consults two facts after the password verifies: is WebAuthn
configured (`webauthn_rp_id` set) and does the user hold ≥1 credential?
Not configured, or zero credentials → session issued immediately
(byte-for-byte today's response shape plus nothing). Configured **and** ≥1
credential → ticket + two-phase.
The `passkey_enrolled` flag also lands on `GetOnboardingStatus` (§7) so the
login page knows *before* submitting whether step 2 exists, and the pre-auth
guard page (RUN-243 chain) keeps failing closed on errors.

### 4.2 Enrollment starts with a password re-check

`BeginPasskeyEnrollment` requires the **current password** in its request and
verifies it with one bcrypt comparison before minting the ceremony. Threat:
a stolen-but-live session (cookie exfiltrated from a sleeping laptop,
malicious extension) must not be able to mint a *new* persistent factor. The
session alone is proof of "was authenticated"; the password re-check
restores "is authenticated" for a factor-granting action. Password-protected
enrollment is the same posture as `ChangePassword` (docs/32 §4.4).

### 4.3 Assertion failures feed the existing rate limiter

docs/32 §4.2 explicitly reserved this: `FinishPasskeyAssertion` failures
(bad signature, expired/unknown ticket, UV missing) increment the same
durable limiter keyed `username + client IP`, with the same 5-in-15
threshold and backoff. The ticket binds the attempt to a user, so the limiter
key is well-defined. `BeginPasskeyAssertion` with an unknown/expired ticket
counts as a failure for the presenting IP.

### 4.4 Session control parity

Passkey-issued sessions are ordinary sessions: they appear in the Security
tab list, are revocable, ride the same two clocks, and `ChangePassword`'s
revoke-others sweep kills them like any other (docs/32 §3.5/§4.4). Removing
a credential does **not** revoke sessions issued earlier through it — session
revocation is the lever for that (stated explicitly in the UI copy for
removal).

## 5. Hard invariants

1. **No downgrade:** if the user holds ≥1 credential and WebAuthn is
   configured, `Login` MUST NOT return a session without a completed
   assertion — enforced in the shared issuance path, never in the UI.
2. **One challenge, one use:** every challenge is single-use, server-generated,
   3-minute TTL; replay or reuse is a hard failure and a limiter event.
3. **Server-side ceremony state only:** challenge bytes and SessionData never
   cross the wire in either direction (leakage-test asserted, §10).
4. **UV required:** assertions with `flags.UV=false` are rejected as if the
   signature were bad (same audit action).
5. **Audit carries no credential material:** no credential IDs, no challenge
   bytes, no attestation objects — extend the existing leakage test.

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
rpc BeginPasskeyAssertion(BeginPasskeyAssertionRequest)
    returns (BeginPasskeyAssertionResponse);
rpc FinishPasskeyAssertion(FinishPasskeyAssertionRequest)
    returns (LoginResponse);          // same message the password login returns

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

message BeginPasskeyAssertionRequest { string assertion_ticket = 1; }
message BeginPasskeyAssertionResponse {
  bytes public_key_options_json = 1;   // protocol.CredentialAssertion JSON
}
message FinishPasskeyAssertionRequest {
  string assertion_ticket = 1;
  bytes assertion_response_json = 2;   // PublicKeyCredentialJSON (authentication)
}
```

`LoginResponse` gains:

```proto
bool passkey_required = N;        // true ⇒ no token in this response
string assertion_ticket = N + 1;  // opaque, 3 min, single-use
```

`GetOnboardingStatusResponse` gains `bool passkey_enrolled` (true iff
WebAuthn configured AND the user holds ≥1 credential) — the pre-auth status
field docs/32 §2.4 reserved; the login page reads it to decide whether step 2
exists.

## 8. Audit

New actions on the existing `audit_logs` (vocabulary docs/32 §5.1 extended
exactly as reserved):

| action | when | details |
|---|---|---|
| `auth.passkey.enrolled` | Finish success | label, transports, backup flags |
| `auth.passkey.enroll_failed` | Finish failure | coarse reason (`bad_attestation`, `password_mismatch`, `ceremony_expired`) |
| `auth.passkey.removed` | DeletePasskey | label |
| `auth.passkey.renamed` | RenamePasskey | — |
| `auth.passkey.assertion_failed` | Finish failure | coarse reason (`bad_signature`, `uv_missing`, `ticket_invalid`, `counter_regression`) |
| `auth.passkey.clone_warning` | counter regression | credential row id |
| `auth.login_success` | (existing) | gains `method: "password" \| "passkey"` |

Rate-limited assertion attempts keep `auth.rate_limited` with the new
`method: "passkey"` detail. The leakage test extends: audit rows must not
contain credential IDs, challenge bytes, or attestation objects.

## 9. Server layout

One self-contained module, mirroring the docs/32 §2.4 prediction:

- `internal/server/passkey.go` — RPC handlers; ceremony store; ticket store;
  the `*http.Request` synthesis adapter for the library's Finish steps;
- `internal/server/passkey_test.go` — full ceremony tests using the library
  against a software authenticator (the `descope/virtualwebauthn` helper
  package, dev-only dependency) so registration/assertion crypto is
  exercised for real, not mocked;
- `internal/keys` — **unchanged** (§3.6);
- `internal/db` — queries + migration 012;
- `internal/config` — `webauthn_rp_id`, `webauthn_origins`,
  `passkey_required` + startup validation.

Config additions:

| Key (env `SUPERVISOR_*`, flag) | Default | Notes |
|---|---|---|
| `webauthn_rp_id` | *(empty)* | empty ⇒ feature fully off |
| `webauthn_origins` | `https://<rp_id>` | CSV; validated against RP ID at startup |
| `passkey_required` | `false` | when true, assertion is mandatory (§5.1); startup-validated to require `webauthn_rp_id` |

`passkey_required = true` semantics: login is *always* two-phase for every
user with credentials; a user with zero credentials cannot exist in practice
(single admin, who enrolls through the settings UI while password login
still works — the flag is meant to be flipped *after* enrollment). The UI
warns when flipping it in settings before any passkey exists.

## 10. Security implications (threat walk)

| threat | mitigation |
|---|---|
| Password phishing/harvest | assertion is origin-bound; a phished password alone yields a ticket that dies in 3 min without the physical factor (§4.1) |
| Stolen session adds attacker passkey | enrollment Begin requires current password (§4.2) |
| Challenge substitution / ceremony mix-up | SessionData server-side, single-use, per-user isolation (§3.4, §5.3) |
| Assertion replay | challenge single-use + TTL + limiter feed (§5.2) |
| Cloned authenticator | sign-counter monotonic policy, fail closed + flag (§3.8) |
| UV-less assertion (e.g. fingerprint bypassed) | UV flag enforced server-side (§5.4) |
| Downgrade to password-only | §5.1 invariant, enforced in issuance path |
| DB theft | public keys only — useless without the authenticator (§3.6) |
| Online brute force of ticket | 256-bit CSPRNG + 3-min TTL + single-use + IP limiter (§4.3) |
| CSRF on ceremony RPCs | Connect content-type requirement + `SameSite=Strict` cookie (docs/32 §2.2); assertion path additionally ticket-gated |
| DoS via ceremony state | Begin is ticket-gated; one live ceremony per user; TTL eviction |
| Lockout | password remains a complete factor when `passkey_required=false`; emergency escape documented (§12) |

## 11. Testing & verification

- **Go unit** (`internal/server/passkey_test.go`): full enroll+assert
  ceremonies via the virtual authenticator; ticket TTL, single-use, IP
  binding; downgrade invariant (credential exists ⇒ password-only login
  returns ticket, never a session); UV-missing rejection; counter
  regression → clone warning + rejection; unconfigured ⇒ endpoints are
  `FailedPrecondition`, `Login` unchanged; origin validation at startup;
  leakage test extension (§8).
- **Web unit** (Vitest): login two-step component states (ticket branch,
  error mapping of `NotAllowedError` to a retryable message); Security-tab
  passkey list rendering incl. sync/clone chips; enrollment flow happy path
  with a mocked ceremony client.
- **E2E** (Playwright, Chromium): CDP `WebAuthn.enable` + virtual
  authenticator — enroll on the Security tab, log out, two-step login with
  the virtual passkey, wrong-key rejection, `GetOnboardingStatus` flag
  flips. Runs with `webauthn_rp_id=localhost` in the E2E compose env (the
  §3.5 localhost carve-out makes this self-contained).
- **Gates**: full chain per AGENTS.md before push.

## 12. Implementation phasing (Linear issues after doc merge)

1. **Backend** — migration 012, config + validation, ceremony/ticket stores,
   proto + handlers, audit, Go tests (virtual authenticator). Feature ships
   dark (no UI) but wire-complete behind the config flag.
2. **Frontend + E2E** — login two-step, Security-tab passkey management,
   Vitest, Playwright CDP flows, `GetOnboardingStatus` flag consumption.

Split keeps each PR reviewable; neither is human-testable meaningfully
without the other, so both land before the owner does passkey manual checks.

## 13. Open questions (owner)

1. **`passkey_required` default** — shipped `false` (recommended on). Comfortable making the Security tab nag until enrolled or dismissed?
2. **RP ID guidance** — the doc recommends a stable MagicDNS/reverse-proxy hostname. OK to make the E2E + docs assume `localhost` for dev and a hostname for prod, with no IP-origin support at all?
3. **Multiple passkeys** — design allows N per user (recommended: enroll 2). Cap at, say, 8, or uncapped?
4. **Sequencing vs RUN-236 (viewer role)** — schema is multi-user-ready; if viewer lands first, passkey scoping is already correct. Preference on which PR series goes first?
