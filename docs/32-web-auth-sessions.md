# Web Authentication — Opaque DB-Backed Sessions & Hardened Login

Status: **Design Phase** (docs-only PR; implementation tracked on Linear after merge — RUN-229).

Runnero's web control plane currently authenticates the administrator with a
JWT-in-cookie whose claims are mirrored into a `sessions` row for revocation.
A production-grade reference guide for generic authentication
(`dmanager/docs/generic-auth-guide.md` — requirements and `MUST`/`MUST NOT`
invariants, adapted here to runnero's stack) exposes a list of concrete gaps
in that design. This doc proposes the changes needed to close them, preserving
runnero's existing product behavior (single admin, bootstrap wizard, ConnectRPC
transport) unless explicitly flagged.

## 1. Problem — current state vs the guide

| # | Invariant (guide) | Current state (`internal/server/auth.go`) | Gap |
|---|---|---|---|
| G1 | Opaque 32-byte random token, hash-at-rest | HS256 JWT (signed with derived HMAC secret) + SHA-256(token) row | JWT signature adds statefulness-free complexity; revocation already DB-backed — the signature buys nothing |
| G2 | Two clocks: sliding idle timeout + absolute cap, both configurable, cap fixed at creation | Single `expires_at = now + 24h`, never renewed | Stolen cookie valid ≤24h; abandoned sessions die at 24h; nothing configurable |
| G3 | Sliding renewal past half the idle window, clamped at the absolute cap | None | Active users are logged out mid-work every 24h |
| G4 | Server-side deletion is the only logout | **No Logout RPC** — frontend `useLogout` clears the react-query cache only; the session row survives | Client-side "forgetting" without server invalidation — explicit guide anti-pattern |
| G5 | Rate limiting: username+IP key, sliding 15 min, 5 failures → exponential backoff | None | Unlimited online brute force; bcrypt cost is the only brake |
| G6 | Unknown-username logins must still run a hash comparison (dummy hash) | `GetAdminUserByUsername` failure returns before any bcrypt work | Timing oracle distinguishes "no such user" from "wrong password" |
| G7 | Cookie: HttpOnly + SameSite=Lax + `Max-Age` = idle deadline; Secure auto/always/never | HttpOnly + SameSite=**Strict** + Secure bool, `Max-Age` = 24h fixed | Strict vs Lax is a deviation (kept — see §2.2); Secure is a bool, no proxy awareness |
| G8 | Trusted-proxy config gates `X-Forwarded-For` trust | No proxy handling; audit events carry no source IP | Rate limiting and audit need a correct client IP |
| G9 | Procedure→role matrix, fail-closed, coverage test; one enforcement point | `IsPublicProcedure` allowlist + everything-else-authenticated-as-admin | No classification of *what* a procedure requires; no viewer concept; new RPCs default to authed-admin silently |
| G10 | Session visibility & control (list/revoke own sessions, device label) | None | User cannot see or kill their other sessions |
| G11 | Auth decision points write audit events incl. IP; retention purge | `audit_logs` exists with auth actions but no IP; no purge job for it | Table grows unbounded; forensics lack provenance |
| G12 | Password policy at every set path (NIST 800-63B: min length, no composition/rotation); configurable bcrypt cost | Class-C 10-char policy in the form layer; `bcrypt.DefaultCost` (10) hardcoded | Cost not configurable; policy below the guide's 12-char floor |

Non-goals (this design): passkeys/WebAuthn (§2.4), external IdPs, TOTP, multi-user
management UI (§2.3), breached-password checks (phones out; stays off), change of
the runner-side auth model (runner registration tokens are unaffected).

## 2. Architecture decisions

### 2.1 Adopted from the guide (verbatim invariants)

| Decision | Choice |
|---|---|
| Token model | Server-side sessions in SQLite; opaque 32-byte `crypto/rand` token, hex-encoded; only SHA-256(token) stored (existing `HashToken` reused) |
| Transport | HttpOnly cookie only. The `Authorization: Bearer` fallback is **removed** (§2.5) |
| CSRF | Cookie `SameSite` + POST-only mutations (Connect unary/streaming over POST); no CSRF-token ceremony |
| Password 2FA | None in this design; passkeys deferred (§2.4) |
| Enforcement | One Connect interceptor owns authentication + authorization for every procedure, unary and streaming; handlers never re-check |
| Failure semantics | Unknown user vs wrong password are indistinguishable (same error, same timing); hash comparison always runs |

### 2.2 Deliberate deviation: keep `SameSite=Strict`

The guide mandates `SameSite=Lax` as the CSRF defense. Runnero can keep the
stronger **Strict**: every mutation is a same-origin POST from the SPA (no
state-changing GETs anywhere in the API), and the UI has no cross-site
navigation entry-point requirement that Lax would serve — an admin arriving
from an external link is not *worse* off; they simply re-authenticate if the
cookie was withheld. Strict blocks cross-site GET-carried cookies in addition
to POST, so it dominates Lax for runnero's threat model. The guide's Lax rule
exists to keep third-party-link UX working; that trade is rejected here.
Flagged as a deviation in docs/05 so future readers don't "fix" it toward Lax
without revisiting this rationale.

### 2.3 Roles: foundation now, viewer users later

The guide's three buckets (`unauthenticated` / `viewer` / `admin`) and its
static procedure→role map are adopted **now**, with runnero's current product
shape preserved: only the bootstrap admin exists, so every authenticated
procedure is classified `admin` (reads included — one admin means reads and
writes share a bucket). The `viewer` bucket is defined in the matrix and
reserved; a future "observer users" feature (user management UI, role
assignment, per-user session/passkey scoping) is a separate design. What this
design ships:

- `admin_users.role TEXT NOT NULL DEFAULT 'admin'` column;
- a static `procedureRole` map in the interceptor covering **every** RPC in
  `proto/api.proto` (public: SetupAdmin, Login, GetOnboardingStatus, GetAuthStatus;
  admin: everything else);
- fail-closed behavior: a procedure missing from the map → `CodeInternal`
  rejection, plus a Go test asserting every generated Connect procedure name
  is classified exactly once (generated-procedure enumeration from
  `supervisorv1connect`, so a new RPC cannot ship unclassified);
- admin-check denial → `PermissionDenied` logged with username + audit entry
  (structure lands now; reachable only if a non-admin user ever exists).

### 2.4 Passkeys: deferred, schema-accommodated

WebAuthn is the guide's preferred 2FA, but it is a self-contained module
(RP ID/origin pinning, credentials + challenges tables, ceremony endpoints,
frontend ceremony client). Folding it into this change would double the review
surface of an already security-critical PR. Deferred to a follow-up design;
this design only ensures nothing here precludes it (session issuance stays one
shared code path; audit vocabulary leaves room for passkey events).

### 2.5 Bearer header: removed

`ExtractSessionToken`'s `Authorization: Bearer` fallback has no caller in the
repo (frontend and tests use the cookie). Keeping two transports doubles the
places a token can leak (proxies, referrer-adjacent bugs, logs). Browser
clients use the cookie exclusively; scripted API use goes through a cookie
jar. Removing it also lets the interceptor treat "no cookie" as the single
unauthenticated shape. (If a concrete scripting need arises later, it should
come back as a deliberate, documented decision — not a silent fallback.)

## 3. Session lifecycle

### 3.1 Two clocks

| Clock | Default | Purpose |
|---|---|---|
| Idle (sliding) `session_idle_timeout` | 168h (7d) | Kills abandoned sessions; slides on activity |
| Absolute cap `session_absolute_timeout` | 720h (30d) | Bounds stolen-cookie lifetime; fixed at creation, never extended |

Both configurable (§6); `absolute ≥ idle` validated at config load. The
remember-me second tier is **dropped**: with both clocks configurable and 7d
idle default, a "remember me" checkbox adds a second value pair and UI for a
setting the owner can trivially change (flagged OQ-4; adding the tier later is
additive — a boolean column + longer pair).

### 3.2 Issuance (one shared path)

`SetupAdmin` and `Login` (and any future login method) converge on one
`issueSession(user, userAgent, cfg)`:

1. 32 bytes from `crypto/rand`, hex-encoded (256-bit token; the JWT nonce and
   signing secret disappear);
2. insert `sessions` row: `token_hash`, `user_id`, `user_agent`,
   `expires_at = now + idle`, `last_seen_at = now`, `absolute_expires_at = now + absolute`;
3. `Set-Cookie` per §3.4.

### 3.3 Validation & sliding renewal (interceptor)

1. No cookie → `Unauthenticated`.
2. Unknown `token_hash` → `Unauthenticated`.
3. `now > absolute_expires_at` → delete row, `Unauthenticated`.
4. `now > expires_at` → delete row, `Unauthenticated`.
5. User lookup fails (deleted) → `Unauthenticated`.
6. Inject user + session ID into context.
7. Sliding renewal: `if now > expires_at − idle/2` → `expires_at =
   min(now + idle, absolute_expires_at)`, `last_seen_at = now`. The half-window
   test keeps DB writes off the hot path; the clamp keeps the absolute cap
   unforgeable.

Public procedures short-circuit before token parsing but still upgrade the
context when a valid cookie is present (login page personalization), matching
the guide.

### 3.4 Cookie

```
session_token=<token>; Path=/; HttpOnly; SameSite=Strict; Max-Age=<idle seconds>[; Secure]
```

- Name unchanged (`session_token`) so the frontend transport keeps working;
- `Max-Age` = the *idle* timeout so browser expiry tracks the server row
  (renewal slides the server row; the browser deadline re-arms on the next
  response that sets the cookie — see §7 note on cookie refresh);
- `Secure`: three-mode config `auto` (default) / `always` / `never`. `auto`
  sets the flag iff the request arrived HTTPS — direct TLS or
  `X-Forwarded-Proto: https` **behind a configured trusted proxy** (§4.1);
- No `__Host-` prefix (plain-HTTP LAN installs must keep working);
- Logout sets the same cookie empty with `Max-Age=0`.

### 3.5 Logout & session control

New RPCs (all admin-bucket, all operating only on the calling user's rows):

- `Logout` — delete the caller's session row + clear cookie. The frontend
  `useLogout` becomes a real RPC call (cache-clearing stays as optimistic UI);
- `ListSessions` — the caller's rows: device label (parsed user-agent),
  created, last-seen, both expiries, `is_current`;
- `RevokeSession(id)` — one of the caller's sessions (revoking the current
  one ≡ logout);
- `RevokeOtherSessions` — all the caller's rows except the current.

The single-admin model means "other users' sessions" cannot exist yet; the
scoping is still enforced in SQL (`WHERE user_id = caller`) so multi-user
later cannot regress it silently (query-level guard, asserted by test).

## 4. Hardened login

### 4.1 Client IP extraction

New `trusted_proxy` config (bool, default false). When true, the client IP is
the first entry of `X-Forwarded-For`; otherwise the socket remote address.
Without the flag the header is never trusted. The extracted IP feeds rate
limiting and audit events only — never authorization.

### 4.2 Rate limiter

In-memory (single-process deployment; documented limitation: counters reset on
restart, audit history persists):

- key: `username + client IP`; window: sliding 15 minutes; threshold: 5 failures;
- backoff: exponential 1, 2, 4… minutes capped at 15, surfaced as
  `ResourceExhausted` with "try again in N seconds";
- success resets the username's counter; locked responses audit
  `auth.rate_limited`;
- feeds from: password failures (both unknown-user and wrong-password —
  equalized), and (future) passkey ceremony failures.

### 4.3 Timing equalization

A bcrypt hash is generated once at startup with the configured cost and kept
in memory. Unknown username → run `bcrypt.CompareHashAndPassword(dummyHash,
password)` and discard the result, then fall through to the identical
"invalid username or password" path. No early return before a hash comparison
has run. The two failure paths share one audit action (`auth.login_failed`,
reason coarse-grained to `invalid_credentials` — the current
`user_not_found`/`invalid_password` split is precisely what the guide forbids
revealing; kept internally distinguishable only by the rate-limit key).

### 4.4 Password policy & hashing

- Keep bcrypt; cost configurable `bcrypt_cost` (default **12**, validated
  range 4–31), applied to new/changed passwords only — stored hashes compare
  against whatever cost they carry, never re-hashed on login;
- class-C password policy floor moves 10 → **12** characters at every set path
  (SetupAdmin now; password change later). No composition rules, no forced
  rotation. `LoginRequest` keeps its deliberate no-pattern validation (it must
  accept whatever SetupAdmin once accepted — docs/30 §5.2).

## 5. Audit & maintenance

### 5.1 Auth events on `audit_logs` (no new table)

Runnero already has an `audit_logs` event table of the right shape
(`user_id` nullable, `action`, `resource`, JSON `details`). The guide's
dedicated `auth_events` table is rejected in favor of extending this one:

- new nullable `source_ip` column, populated at auth decision points;
- auth action vocabulary: `auth.login_success`, `auth.login_failed`,
  `auth.logout`, `auth.session_revoked`, `auth.setup_admin`,
  `auth.rate_limited` (renames from today's `auth.login`;
  `auth.login_failed.reason` narrows to coarse values only);
- MUST NOT contain credentials, tokens, or challenge bytes (already an
  asserted property — `internal/db/leakage_test.go` extends to the new fields).

Read path: the existing audit read surface (admin-only today) gains an IP
column; no per-viewer scoping needed until multi-user exists.

### 5.2 Background maintenance

One hourly job (supervisor goroutine; failures log a warning, never crash):
purge `sessions` past either clock, and `audit_logs` older than
`audit_retention` (default 90d, configurable). The job reuses the existing
sweeper pattern (log retention) and is idempotent.

## 6. Configuration

| Key (env `SUPERVISOR_*`, flag) | Default | Notes |
|---|---|---|
| `session_idle_timeout` | `168h` | Sliding window |
| `session_absolute_timeout` | `720h` | ≥ idle (validated) |
| `secure_cookies` | `auto` | replaces the bool; `auto`/`always`/`never` |
| `bcrypt_cost` | `12` | 4–31 |
| `trusted_proxy` | `false` | enables `X-Forwarded-For` trust |
| `audit_retention` | `2160h` (90d) | purge horizon |

The derived JWT signing secret (`internal/keys` LabelJWTSigning) is retired —
the master-key derivation collapses to the DB encryption key only, and
docs/05's "two derived secrets" wording updates accordingly. Existing env
`SECURE_COOKIE=true|false` keeps back-compat parsing (`true`→`always`,
`false`→`auto`) with a deprecation note.

## 7. Frontend contract

- `GetSession` remains the session-restoring call; **no new status endpoint**: the
  public `GetOnboardingStatus` already reports `needs_setup`, and passkeys would
  add their own status field to it when they land (§2.4);
- login form: unchanged fields (no remember-me checkbox per §3.1); locked-out
  users see the backoff message;
- new **Security** tab (settings): session list with device labels, current
  marker, revoke / revoke-others; confirmation modal per existing destructive
  patterns;
- `useLogout` calls the Logout RPC; any `Unauthenticated` response clears the
  cached user and routes to login (existing behavior, made exhaustive via the
  connect-es error hook);
- the cookie remains the only credential: nothing token-shaped in
  localStorage (already true; asserted by a lint-level grep test if cheap).

Cookie refresh note: `Max-Age` tracks the idle deadline at issuance; sliding
renewal extends the server row only. The browser cookie may therefore expire
while the server row lives (after ~idle/2 of inactivity past the initial
window). The interceptor's renewal path re-sets the cookie on renewal so an
active browser always carries a fresh `Max-Age` — renewal responses include
`Set-Cookie` with the updated deadline.

## 8. Data model changes (migration 009)

```sql
-- sessions: two clocks + observability
-- Constant sentinel defaults only: SQLite rejects non-constant defaults on
-- ALTER TABLE ADD COLUMN for any table that already holds rows, so a
-- populated production database cannot take DEFAULT CURRENT_TIMESTAMP here.
-- Rows created afterwards set every column explicitly (CreateSession).
ALTER TABLE sessions ADD COLUMN user_agent TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN last_seen_at DATETIME NOT NULL DEFAULT '1970-01-01 00:00:00';
UPDATE sessions SET last_seen_at = created_at;                  -- last seen at issuance
ALTER TABLE sessions ADD COLUMN absolute_expires_at DATETIME NOT NULL DEFAULT '1970-01-01 00:00:00';
-- expires_at redefined as the sliding idle deadline
UPDATE sessions SET absolute_expires_at = expires_at;           -- old tokens die at old expiry
-- admin_users: role foundation
ALTER TABLE admin_users ADD COLUMN role TEXT NOT NULL DEFAULT 'admin';
-- audit_logs: provenance
ALTER TABLE audit_logs ADD COLUMN source_ip TEXT;
```

Pre-existing session rows are **not** invalidated by upgrade. Old rows keep
`expires_at` (their original ≤24h deadline) and gain `absolute_expires_at =
expires_at`, so they live out their natural lifetime and die — the new
interceptor validates by `token_hash` lookup only (no JWT parsing), and the
row's existence is the validity proof, so in-flight JWT cookies keep working
until their original expiry and then never come back. No forced re-login;
PR upgrade notes still call out the one-time model switch.

## 9. Protocol changes (`proto/api.proto`)

```proto
service AuthService {
  rpc SetupAdmin(...)   // unchanged shape
  rpc Login(LoginRequest)        // unchanged wire shape
  rpc GetSession(...)            // unchanged
  rpc Logout(LogoutRequest) returns (LogoutResponse);
  rpc ListSessions(ListSessionsRequest) returns (ListSessionsResponse);   // caller's rows only
  rpc RevokeSession(RevokeSessionRequest) returns (RevokeSessionResponse);
  rpc RevokeOtherSessions(RevokeOtherSessionsRequest) returns (RevokeOtherSessionsResponse);
}

message SessionInfo {
  string id = 1;            // session row id, not the token
  string device_label = 2;  // parsed from user-agent
  google.protobuf.Timestamp created_at = 3;
  google.protobuf.Timestamp last_seen_at = 4;
  google.protobuf.Timestamp expires_at = 5;
  google.protobuf.Timestamp absolute_expires_at = 6;
  bool is_current = 7;
}
```

`GET`-safe procedures only exist as Connect POSTs; no wire-semantics change for
streaming (auth interceptor already wraps streaming handlers).

## 10. Security implications

- **XSS**: token unreadable by JS (HttpOnly, cookie-only transport); a token in
  localStorage was never possible and stays impossible;
- **CSRF**: SameSite=Strict + POST-only mutations; no state-changing GET;
- **Token theft**: absolute cap bounds lifetime even with a stolen cookie;
  revocation is a row delete (list/revoke UI makes it user-accessible);
- **Brute force**: per-username+IP exponential lockout; bcrypt cost raised;
  timing equalized; lockout events audited;
- **Reveal-minimization**: identical errors/timing for unknown-user and
  bad-password; audit reasons coarse; no credentials/tokens in logs (leakage
  tests extended);
- **Proxy spoofing**: `X-Forwarded-For` trusted only behind `trusted_proxy`;
  without it, attacker-supplied headers cannot poison rate-limit keys or audit
  IPs;
- **Fail-closed enforcement**: unknown procedure → internal error, coverage
  test in CI, so auth-parsing drift cannot silently open or close endpoints.

## 11. Testing & verification (guide §12 mapped)

- Sliding-renewal boundary math: no write before half-window; clamp at cap;
- either-clock expiry → row deleted + `Unauthenticated`;
- unknown-username vs wrong-password statistically equal time (bcrypt runs
  both paths — test asserts the dummy-comparison path executes);
- rate limiter transitions: 5th failure locks, backoff doubles, success
  resets, lock expiry unlocks;
- role-matrix coverage test: every generated procedure classified exactly
  once; unclassified → rejected;
- denial path audits (structure test until multi-user exists);
- logout deletes the row; subsequent request with the same cookie fails;
- `ListSessions` scoping (SQL-level guard);
- cookie assertions (HttpOnly, SameSite=Strict, Max-Age, Secure per mode) at
  Login/SetupAdmin/Logout/renewal;
- E2E: login → use → logout → reuse fails; security-tab revoke flow;
- existing suites green: interceptor tests reworked, onboarding/setup tests,
  frontend session tests.

## 12. Implementation phasing (issues after doc merge)

1. **Sessions v2 core** — migration, issuance path, two clocks + renewal,
   cookie modes, JWT removal, upgrade invalidation; docs/05+07+08 updates;
2. **Hardened login** — rate limiter, IP extraction, dummy-hash timing, audit
   vocabulary + retention purge, config keys;
3. **Session control surface** — Logout/List/Revoke RPCs, Security tab,
   frontend auth-context hardening, E2E.

Phases 1–2 are separable; phase 3 depends on 1. Role-matrix/fail-closed
coverage lands inside phase 1 (it rewrites the interceptor anyway).

## 13. Open questions (owner)

- **OQ-1** Bearer removal: any out-of-repo scripts automating the API via
  `Authorization: Bearer` today? (Recommendation: remove; cookie-jar scripts.)
- **OQ-2** SameSite Strict kept (deviation from the guide's Lax mandate) —
  accept the deviation rationale (§2.2)?
- **OQ-3** Remember-me tier dropped (both clocks configurable instead) —
  acceptable, or required UX?
- **OQ-4** Password policy floor 10 → 12 chars for new/changed passwords
  (guide/NIST; existing passwords grandfathered) — OK?
- **OQ-5** Viewer-role foundation ships as column + matrix only, with the
  observer-users feature (management UI, role assignment) as a separate future
  design — OK, or is multi-user wanted in this scope?
