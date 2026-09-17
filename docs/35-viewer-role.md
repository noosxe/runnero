# Viewer Role & Multi-User Management

Status: **Design Phase** (docs-only PR; implementation tracked on Linear — RUN-236).

Runnero's web control plane has been single-account since bootstrap: one admin
user, every authenticated procedure classified `admin` in the procedure→role
matrix (docs/32 §2.3), and the `viewer` bucket reserved but unreachable. The
foundation shipped deliberately unused: `admin_users.role` (migration 009), a
fail-closed coverage test over every generated procedure, per-user session
scoping enforced in SQL (`WHERE user_id = caller`, docs/32 §3.5), and audit
rows keyed to the acting user. This design activates the foundation: real
viewer accounts, an admin-only user-management surface, and a reclassified
procedure matrix that lets a second class of user observe the fleet without
being able to touch it.

## 1. Problem

| # | Limitation today | Consequence |
|---|---|---|
| P1 | The bootstrap admin is the only account | Observability (CI health, job logs, pool status) requires handing out the full admin key — the same credential that can delete pools, terminate runners, and read auth-profile tokens |
| P2 | `admin_users.role` exists but nothing assigns or enforces non-admin roles | The `viewer` bucket in `procedureRoles` is dead code; docs/32 §2.3's reserved surface is unused |
| P3 | No user lifecycle surface | A teammate cannot get an account at all; the only "delegation" pattern is credential sharing (worst practice: shared identity in audit rows, shared lockout) |
| P4 | Role changes, once possible, need semantics | Demote-the-last-admin and delete-yourself are footguns that must be structurally impossible, not UI-discouraged |

Non-goals: external IdP/OIDC (README roadmap), per-resource ACLs (a viewer
sees everything observable or nothing — no per-pool scoping), user rename
(v1 keeps usernames immutable; see OQ-4), admin view/control of *other*
users' sessions (OQ-5), TOTP/2FA beyond passkeys (docs/34).

## 2. Architecture decisions

### 2.1 Two authenticated roles, enforced live

Roles stay exactly what migration 009 planted: `admin_users.role ∈
{admin, viewer}`, default `admin`. The interceptor already resolves the user
row from the session on **every** request (docs/32 §3.3 step 5) and carries
`user.Role` in the request context — so this design needs no session-schema
change and gets one crucial property for free: **role changes take effect on
the caller's next request**. There is no role cache, no role claim in a
token, no staleness window to reason about; a demoted admin's live session
downgrades silently mid-conversation, and a promoted viewer gains surface
without re-login.

The matrix gains `bucketViewer` between the existing buckets:

| Bucket | Requires |
|---|---|
| `bucketPublic` | nothing (unchanged) |
| `bucketViewer` | any valid session (admin **or** viewer) |
| `bucketAdmin` | valid session + `role == admin` (unchanged semantics) |

Fail-closed stays: unclassified procedure → `CodeInternal`; the coverage test
asserting every generated Connect procedure is classified exactly once
extends automatically to the new UserService RPCs and gains explicit
bucket-value assertions for the reclassified set below, so a future refactor
cannot silently promote viewer procedures to admin (or vice versa).

### 2.2 Reclassified matrix (complete)

The viewer bucket splits into two families — *self-service* (rows that are
already SQL-scoped to the calling user, so "viewer may call" cannot leak
another user's data) and *read-only observability* (the product reason a
viewer exists).

**Unchanged public:** `SetupAdmin`, `Login`, `GetOnboardingStatus`,
`BeginPasskeyLogin`, `FinishPasskeyLogin`.

**Admin → viewer (self-service; scoping already enforced in SQL and
asserted by tests):**

| Procedure | Why safe for viewers |
|---|---|
| `GetSession` | returns the caller's own identity + role |
| `Logout`, `ListSessions`, `RevokeSession`, `RevokeOtherSessions` | `WHERE user_id = caller` on every query (docs/32 §3.5) |
| `ChangePassword` | operates on the calling user; revokes only the caller's other sessions |
| `BeginPasskeyEnrollment`, `FinishPasskeyEnrollment`, `ListPasskeys`, `RenamePasskey`, `DeletePasskey` | per-user ceremonies and ownership-scoped CRUD (docs/34 §6); see §2.5 |

**Admin → viewer (read-only observability):**

| Procedure | Rationale |
|---|---|
| `ListPools`, `WatchPools`, `ListRunners`, `WatchRunners` | fleet status is the viewer's core read |
| `GetJobHistory`, `GetJobRecord`, `GetSystemStats`, `WatchDashboard` | CI health/analytics |
| `GetRunnerLogs`, `StreamRunnerLogs` | workflow output; the reason to have an observer at all |
| `GetRenovateStatus`, `ListRenovateHistory`, `ListImageUpdates` | read-only maintenance visibility |

**Stays admin (writes, secrets, infrastructure internals):**

| Procedure | Why not viewer |
|---|---|
| `CreatePool`, `UpdatePool`, `DeletePool`, `TerminateRunner`, `DiscoverTargets` | fleet mutations + network probing |
| `ListAuthProfiles`, `CreateAuthProfile`, `UpdateAuthProfile`, `DeleteAuthProfile` | auth profiles guard runner-registration secrets; even metadata stays admin |
| `GetAppSettings`, `SetAppSetting` | global configuration read+write (OQ-3) |
| `CompleteOnboarding` | bootstrap wizard |
| `ListSupervisorLogs`, `StreamSupervisorLog`, `ListRemovalRecords` | supervisor internals — infrastructure-level, not CI-level (OQ-1) |
| `TriggerRenovateRun`, `CheckImageUpdate`, `PullImage`, `DismissImageUpdate` | maintenance actions (registry pulls mutate the Docker host) |
| all `UserService` (new, §2.3) | user management is definitionally admin |

Reads a viewer gets are exactly the reads the dashboards render; the UI can
therefore hide mutation affordances without ever guessing what the server
would allow — and the server, not the UI, is the enforcement point.

### 2.3 User management surface (`UserService`, admin-only)

New service, five RPCs. The creator always supplies the initial password
(no invite/email flow exists in a self-hosted daemon).

```proto
service UserService {
  rpc ListUsers(ListUsersRequest) returns (ListUsersResponse);
  rpc CreateUser(CreateUserRequest) returns (CreateUserResponse);
  rpc SetUserRole(SetUserRoleRequest) returns (SetUserRoleResponse);
  rpc SetUserPassword(SetUserPasswordRequest) returns (SetUserPasswordResponse);
  rpc DeleteUser(DeleteUserRequest) returns (DeleteUserResponse);
}

message UserInfo {
  string username = 1;
  string role = 2;                        // "admin" | "viewer"
  google.protobuf.Timestamp created_at = 3;
}
```

| RPC | Semantics |
|---|---|
| `ListUsers` | all users, `id` order: username, role, created_at. No session or last-login metadata (OQ-5) |
| `CreateUser` | insert with the given role; `AlreadyExists` on username conflict (admin-only surface, so enumeration is not a concern — Login's timing equalization is untouched); password floor 12 wire-side, same annotation as `SetupAdmin` |
| `SetUserRole` | flips `admin_users.role`; **last-admin guard** below; self-demote allowed when another admin exists (UI warns) |
| `SetUserPassword` | admin reset for a locked-out user; verifies floor 12; revokes the target's *other* sessions (same sweep as `ChangePassword`, docs/32 §3.5) so a stolen old password dies with the reset; target's passkeys are untouched (they are the user's own recovery path) |
| `DeleteUser` | deletes the row; `sessions` and `webauthn_credentials` cascade via existing FKs; audit rows keep (`user_id` is `ON DELETE SET NULL`) with the username preserved in event details |

**Structural guard rails** (enforced server-side in one transaction, never
only in the UI):

- **Last-admin invariant:** `SetUserRole` to `viewer` and `DeleteUser` are
  refused with `FailedPrecondition` when the target is an admin and
  `COUNT(*) FROM admin_users WHERE role = 'admin'` is 1. The count reads the
  same transaction that performs the write, so concurrent demotions cannot
  race past the guard.
- **No self-delete:** `DeleteUser` on the calling user → `FailedPrecondition`
  (a second admin must perform it; avoids self-terminating sessions mid-call).

New/changed SQL (queries layer only — **no migration**): `UpdateAdminRole`,
`UpdateUserPassword` (reuse `UpdateAdminPassword`), `CountAdminRoleUsers`
(`WHERE role = 'admin'`), plus `CreateAdminUser`/`DeleteAdminUser` which
already exist. `ListAdminUsers` already returns `*`.

Username validation mirrors `SetupAdmin` today (`min_len = 1`) plus
`max_len = 64`; no charset restriction in v1 (RUN-223's audit may tighten
globally later — one place, `SetupAdmin` + `CreateUser`, then follow).

### 2.4 Role-aware frontend

- `GetSessionResponse` gains `string role = 5`; `is_admin` (field 2) stays,
  computed, so existing consumers keep working while new code switches to
  `role`. The auth context carries `role`; the single `authenticatedRoute`
  guard is unchanged — there are no admin-only *routes* in v1, only
  admin-only *surfaces* inside shared pages.
- **Settings** gains a **Users card** (rendered for admins only): user list
  with role chips, add-user dialog (username, initial password ×2, role
  select), role select per row with a confirm step (self-demote and
  last-admin cases surfaced as explicit warnings; the server remains the
  source of truth), reset-password dialog, delete confirm. The current
  user's row is badged "you" and its delete action is disabled (mirroring
  the server guard).
- **Viewer UX:** mutation affordances (create/edit/delete buttons, run
  buttons, settings/Users cards, supervisor-log and removal tabs) render
  only for admins. A viewer's nav stays full-width read-only: dashboards,
  history, logs (runner tab), pools, renovate status.
- **`PermissionDenied` handling:** the global connect error hook treats
  `PermissionDenied` as *stay logged in, toast "Admin role required"* — the
  opposite of `Unauthenticated` (which routes to login). This is the
  mid-session-demotion path: no destructive logout, no error page, the next
  navigation simply shows the viewer surface.
- The Security tab works identically for viewers (own sessions, own
  passkeys, own password) — per-user scoping was built for exactly this in
  docs/32 §3.5.

### 2.5 Passkey interaction (docs/34 unchanged)

All five passkey ceremony/CRUD RPCs move to the viewer bucket (§2.2) — they
are per-user by construction: enrollment re-checks the *caller's* password,
and credential CRUD is ownership-scoped. Mechanics are untouched:

- A viewer enrolls a passkey exactly like an admin; `FinishPasskeyLogin`
  resolves the user by credential ID and issues a session through the shared
  `issueSession` path — the session simply carries that user's role.
  Passwordless viewer login needs zero new code.
- `DeleteUser` cascades the user's credentials; their passkey then fails
  server-side with the standard unknown-credential path — the login screen
  says "passkey assertion rejected", indistinguishable from any other
  rejection (docs/34 §5.4 property preserved).
- Clone-warning flagging, rate-limit feeds, and audit vocabulary are
  per-user already and need no changes.

One docs/34 clarification lands with this design: passkey login is
role-agnostic by design — the credential proves the identity, the
`admin_users.role` row (read live) proves the privilege.

## 3. Audit

New actions on the existing `audit_logs` table (acting user in `user_id`,
target identified by username in `details` so events survive target
deletion):

| Action | Details |
|---|---|
| `auth.user_created` | `{target_username, role}` |
| `auth.user_deleted` | `{target_username}` |
| `auth.role_changed` | `{target_username, old_role, new_role}` |
| `auth.password_reset` | `{target_username}` (admin reset — distinct from self-service `auth.password_changed`) |

Denials keep the docs/32 §2.3 structure: `PermissionDenied` on any
admin-bucket procedure logs username + procedure. Leakage tests extend to
the new events (no passwords, no tokens).

## 4. Data model changes

**None.** Migration 009 shipped `admin_users.role`; FK cascades for sessions
(001) and credentials (012) and `audit_logs.user_id ON DELETE SET NULL` are
in place; session scoping is enforced query-level. This design consumes the
foundation exactly as docs/32 §2.3 staged it — the doc's OQ-5 split
(foundation now, users later) resolves as planned.

## 5. Security implications

- **Privilege escalation:** the only write paths to `admin_users.role` are
  `SetUserRole`/`CreateUser`, both admin-bucket and covered by the matrix
  coverage test. A viewer calling them gets `PermissionDenied` + audit row.
  There is no self-service path to admin (password change and passkey
  enrollment mutate credentials, never roles).
- **Last-admin invariant** makes lockout-by-demote structurally impossible,
  including via concurrent requests (transactional count). The bootstrap
  admin cannot be locked out by the feature itself; losing *all* admin
  passwords still has the docs/32 bootstrap story (single-admin reset) as
  the recovery of last resort.
- **Live role resolution** means stolen-session blast radius shrinks the
  moment the role changes — no revocation gap, no "wait for the token to
  expire".
- **Viewer trust boundary is explicit:** viewers read job logs (workflow
  output) and fleet topology (pool/runner names, statuses). An operator
  grants viewer only to identities trusted at that level; everything
  secret-shaped (auth profiles, app settings, supervisor internals) stays
  admin. No read RPC is *hidden* from viewers that the UI does not also
  gate, so the mental model is one table (§2.2), not a permission scatter.
- **Username enumeration:** `CreateUser` returns `AlreadyExists`; acceptable
  because the surface is admin-only. `Login` equalization (docs/32 §4.3) is
  untouched — viewer accounts do not change the unauthenticated surface.
- **Rate limiting / lockout:** per-username+IP mechanics apply unchanged to
  viewer accounts; a viewer account is one more brute-force target with the
  same locks around it.
- **CSRF/XSS posture:** unchanged (SameSite=Strict cookie, POST-only
  mutations, HttpOnly). No new transport, no new secrets, no config keys.

## 6. Testing & verification

- Matrix coverage: generated-procedure enumeration still passes with
  `UserService`; explicit assertions pin every §2.2 reclassification.
- Escalation: viewer session calling one representative + every UserService
  admin RPC → `PermissionDenied`, audit row written; same call as admin → OK.
- Guards: demote/delete last admin → `FailedPrecondition` (including a
  transactional race test: two concurrent demotes of two admins, at least
  one refused); self-delete → `FailedPrecondition`; demote-self with a
  second admin → OK.
- Live effect: viewer session → promoted → next request on the *same*
  session hits admin surface; demoted admin's next mutation →
  `PermissionDenied` without re-login.
- Lifecycle: create (floor-12 enforcement, duplicate username) → login as
  viewer → `GetSession.role == "viewer"` → password reset revokes target's
  other sessions but not passkeys → delete cascades sessions + credentials
  (DB assert) → passkey login with the deleted credential → generic
  rejection; audit events present with username details.
- Passkey-viewer: viewer enrolls → passwordless login → lands viewer role.
- Frontend (Vitest): Users card CRUD incl. guard rails, role-gated
  rendering, `PermissionDenied` toast-not-logout.
- E2E (new flow): admin creates viewer → viewer logs in → read-only surface,
  mutation buttons absent → admin promotes mid-session → viewer's UI picks
  up admin surface after refresh; all existing flows stay green (admin
  behavior unchanged when only one user exists).

## 7. Implementation phasing

One implementation PR under RUN-236: matrix reclassification + UserService +
frontend + E2E land together. Splitting the matrix alone would ship a dead
bucket (no way to become a viewer); splitting the UI would ship RPCs no UI
reaches. The feature is invisible until a second user exists, so upgrade
risk is minimal: existing single-admin deployments observe zero change
(their session keeps `admin` semantics via the same live role read).

Docs updates in the implementation PR: docs/07 (no schema change — a note
that `role` is now active), docs/08 (UserService + GetSession.role), docs/09
(Users card, role-aware UX), README moves the entry from Roadmap to
Features.

## 8. Open questions (owner)

- **OQ-1** Supervisor-internal observability (`ListSupervisorLogs`,
  `StreamSupervisorLog`, `ListRemovalRecords`) proposed **admin-only** —
  viewers see CI-level signals (runner logs, dashboards), not daemon
  internals. OK, or should viewers see supervisor logs too?
- **OQ-2** `SetUserPassword` (admin reset for a locked-out user) is in
  scope; without it the only recovery is delete+recreate, which detaches the
  user's audit history (`user_id` → NULL). Keep in v1?
- **OQ-3** `GetAppSettings` stays admin-only (viewer sees no global config,
  not even read-only). OK?
- **OQ-4** Usernames immutable in v1 (no rename); `max_len 64` added for
  `CreateUser` only. OK?
- **OQ-5** No admin view/control of *other* users' sessions in v1 (admins
  manage only their own sessions in the Security tab; deleting a user is the
  blunt instrument for their sessions). Natural follow-up issue if wanted.
