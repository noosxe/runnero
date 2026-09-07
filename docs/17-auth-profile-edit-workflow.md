# 17. Auth Profile Edit Workflow Design

## 1. Problem Statement & Motivation

Auth profiles (Git provider credentials: GitHub App, PAT, Gitea/Forgejo tokens) are the
trust anchors that allow the supervisor to mint ephemeral runner registration tokens.
Today the `AuthProfileService` only supports **Create**, **List**, and **Delete**
(`proto/api.proto`, `internal/server/auth_profile.go`, `web/src/routes/profiles.tsx`).

This forces operators into a destructive workaround for routine credential lifecycle
operations:

| Scenario | Current workaround | Pain |
| :--- | :--- | :--- |
| PAT / token expired or rotated | Delete profile, recreate with new token | Delete is **blocked while a pool references the profile**; operator must first detach or delete healthy pools, causing runner downtime. |
| GitHub App private key rotated (GitHub forces yearly key rotation) | Same as above | Key rotation is a *routine, security-mandated* operation — downtime should never be part of it. |
| Typo in profile name or wrong App ID | Delete + recreate | Loses pool wiring; audit trail shows a delete/create pair instead of an update. |
| Switching auth method (e.g. PAT → GitHub App) | Delete + recreate | Same as above, plus re-installation bookkeeping. |

The database layer is already prepared: sqlc exposes an (unused, untested)
`UpdateAuthProfile` query and the `auth_profiles` table maintains
`created_at` / `updated_at`. Only the RPC surface, service logic, and UI are missing.

## 2. Goals & Non-Goals

### Goals

1. Rename a profile safely, including while referenced by runner pools (pools
   reference by **ID**, so renames are non-breaking).
2. Rotate secrets (private key / token) **without deleting the profile** and without
   touching pool wiring — the primary use case.
3. Allow changing `auth_method` (with strict re-validation) so operators can migrate
   PAT → GitHub App and vice versa without tearing down pools.
4. Keep the **write-only secret model** intact: secrets are never returned by any RPC;
   the UI can never pre-fill an existing secret.

### Non-Goals

- Partial/field-mask updates (`update_mask`): unnecessary — each auth method requires
  exactly one credential kind, so a small "blank = keep" convention (§4.2) covers all
  valid states without mask machinery.
- Secret *read-back* or decryption in the UI (rejected for security reasons, §7).
- Optimistic concurrency (`If-Match` / version column): SQLite is a single-writer
  store and no other RPC in this codebase uses ETags; last-write-wins with an
  authoritative `updated_at` in the response is consistent with existing behavior.
- Automatic propagation of rotated credentials to *running* runner containers (they
  already receive ephemeral registration tokens; profile edits affect future token
  minting only).

## 3. Requirements & Invariants

### 3.1 Functional Requirements

1. `UpdateAuthProfile` RPC updates `name`, `auth_method`, `app_id`, and optionally
   rotates `private_key` / `token`.
2. The edited profile is returned in the response (same read-only shape as Create/List,
   including `has_private_key` / `has_token` indicators and app metadata).
3. A profile referenced by runner pools **can** be edited (rename / re-key / method
   switch) — unlike Delete. Pool wiring is never touched by profile edits.
4. Every successful update records an `auth_profile.update` audit entry with
   before/after **metadata** (name, auth_method, which secret classes changed) and
   never any secret material.
5. Errors: unknown id → `CodeNotFound`; invalid payload → `CodeInvalidArgument`;
   duplicate name → `CodeAlreadyExists`; upstream credential check failure →
   `CodeInvalidArgument` (mirrors Create).

### 3.2 Invariants Preserved

- A profile always holds a valid secret for its `auth_method` (Create guarantees
  this today; Update must never break it — see the blank-means-keep rules in §4.2).
- Secrets are persisted only as AES-256-GCM ciphertext
  (`private_key_encrypted`, `token_encrypted`, docs/05 §5, `internal/db/crypto.go`).
- Raw secrets never appear in logs, audit entries, or RPC responses.

## 4. Protocol Extensions (`proto/api.proto`)

### 4.1 RPC Definition

```proto
service AuthProfileService {
  rpc ListAuthProfiles (ListAuthProfilesRequest) returns (ListAuthProfilesResponse);
  rpc CreateAuthProfile (CreateAuthProfileRequest) returns (CreateAuthProfileResponse);
  rpc UpdateAuthProfile (UpdateAuthProfileRequest) returns (UpdateAuthProfileResponse); // NEW
  rpc DeleteAuthProfile (DeleteAuthProfileRequest) returns (DeleteAuthProfileResponse);
}

message UpdateAuthProfileRequest {
  int64 id = 1;             // Target profile (required, > 0)
  string name = 2;          // New display name (required, non-empty, unique)
  string auth_method = 3;   // "github_app", "gitea_token", "forgejo_token", "pat"
  int64 app_id = 4;         // GitHub App only (required > 0 for github_app)
  bytes private_key = 5;    // Write-only. Empty ⇒ keep existing key (github_app)
  string token = 6;         // Write-only. Empty ⇒ keep existing token (token methods)
}

message UpdateAuthProfileResponse {
  AuthProfile profile = 1;  // Same read-only shape as CreateAuthProfileResponse
}
```

Semantics intentionally mirror `CreateAuthProfileRequest` field-for-field (minus the
missing `id`), so the frontend can share one form component for create and edit and the
`CredentialValidator` interface needs only a small adapter (§5.2).

### 4.2 Secret Update Semantics ("blank = keep")

Because each auth method requires exactly one credential kind, the cross product of
(method, provided secrets) is fully decidable without a field mask:

| `auth_method` | Secret supplied in request | Resulting stored state |
| :--- | :--- | :--- |
| `github_app` | `private_key` present | Re-encrypt + store new key. |
| `github_app` | `private_key` empty | **Keep** existing key (must exist — invariant 3.2; missing ⇒ `CodeFailedPrecondition`, self-heal: see below). |
| `gitea_token` / `forgejo_token` / `pat` | `token` present | Re-encrypt + store new token. |
| `gitea_token` / `forgejo_token` / `pat` | `token` empty | **Keep** existing token (same rule). |

**Auth method switch** (e.g. `pat` → `github_app`): the request must carry the secret
kind required by the *new* method (a switch cannot rely on "keep", because the stored
secret is of the wrong kind). Any residual secret material of the *old* method is
**purged** (columns set to NULL) — stale keys/tokens must never linger in the database.

> **Self-heal note:** if a legacy/degenerate row violates the invariant (e.g.
> `github_app` row without a key), Update requires the missing secret — validation
> errors guide the operator to re-enter it, repairing the row as a side effect.

### 4.3 Field Validation Rules

Identical to Create (`validateCreateAuthProfileRequest`), extracted into a shared
helper so both RPCs stay in lockstep:

- `id > 0`; `name` non-empty after trim; `auth_method` in the allowed set (case-insensitive,
  normalized to lower case like Create).
- `github_app` ⇒ `app_id > 0` **and** (`private_key` provided **or** profile already
  `has_private_key` and request key is empty).
- token methods ⇒ (`token` non-empty **or** profile already `has_token`).
- Method switch ⇒ new method's secret **must** be provided (no "keep" across a switch).

## 5. Backend Implementation Plan

### 5.1 Data Access (`internal/db`)

No schema migration and no new SQL is required. The existing generated
`UpdateAuthProfile` query (full-row `UPDATE … RETURNING *`, sets `updated_at`) is
reused via a **read-merge-write** in the handler:

```text
BEGIN (logical):
  1. GetAuthProfileById(id)          → 404 if missing; capture "before" snapshot
  2. validate + merge request onto snapshot (§4.2 rules)
  3. encrypt any newly supplied secrets (AES-256-GCM, current key)
  4. purge columns not applicable to the (new) method
  5. UpdateAuthProfile(merged row)   → RETURNING *
  6. recordAuditLog("auth_profile.update", before→after metadata)
COMMIT (logical)
```

SQLite serializes writers, so the read-merge-write window is safe in-process; this
matches the existing Delete flow, which also reads then checks pools then deletes.
Name uniqueness: the `UNIQUE(name)` constraint violation from step 5 is mapped to
`connect.CodeAlreadyExists` (detect via `errors.Is(err, sqlite3 constraint)` /
error-string check consistent with sqlc+modernc driver usage).

### 5.2 Service Handler (`internal/server/auth_profile.go`)

- Extend `AuthProfileDatabase` interface with `UpdateAuthProfile(ctx, db.UpdateAuthProfileParams)`.
- Generalize `CredentialValidator` so the existing GitHub/Gitea/Forgejo upstream checks
  can be reused for Update. `ValidateCredentials` today consumes a
  `CreateAuthProfileRequest`; introduce a small provider-agnostic input:

  ```go
  type CredentialCheck struct {
      AuthMethod string
      AppID      int64
      PrivateKey string // PEM; empty when keeping existing
      Token      string
  }

  type CredentialValidator interface {
      ValidateCredentials(ctx context.Context, req *supervisorv1.CreateAuthProfileRequest) error
  }
  ```

  Implementation note: the concrete validator builds a provider via
  `provider.DefaultRegistry.Build` and calls `ValidateCredentials`. For Update, the
  handler performs upstream validation **only when a new secret is supplied** —
  renaming or touching unrelated fields must not burn provider rate limits or fail on
  transient provider outages, and kept (already-validated) secrets are not re-checked.

### 5.3 Generated Code

- `buf generate` regenerates `internal/pb/supervisor/v1` + `supervisorv1connect`
  (`buf.gen.yaml` is already wired); frontend stubs regenerate into `web/src/gen`.

## 6. Frontend UI / UX (`web/src/routes/profiles.tsx`)

### 6.1 Profile Card

Each profile card gains an **Edit** action next to Delete:

```text
+-------------------------------------------------------------------------------------------+
| github-app-prod  [GitHub App]                                    [ Edit ]  [ Delete ]     |
| App ID: 1049281 • Installations: 3                                                        |
| Private Key: [ Configured (AES-256 encrypted) ] • Token: [ Not Applicable ]               |
+-------------------------------------------------------------------------------------------+
```

### 6.2 Edit Modal

The existing create modal is refactored into a shared `AuthProfileForm` used by both
modes (create = empty initial state; edit = prefilled):

- **Prefilled:** `name`, `auth_method`, `app_id`.
- **Secret fields start empty** with helper text
  *"Leave blank to keep the existing key/token"* — the write-only model means the UI
  cannot and must not display the current secret. The card's
  `Configured (AES-256 encrypted)` badge is the only "is a secret set" signal.
- **Auth method dropdown** remains enabled. Changing it clears secret inputs and flips
  their helper text to *"Required when changing the auth method"* (enforces §4.2's
  no-keep-across-switch rule client-side; the server enforces it authoritatively).
- **Submit** calls `useUpdateAuthProfile`; on success the modal closes and the
  profiles query invalidates (list + `onboardingStatus`, mirroring the create hook).
- Server errors (duplicate name, validation, upstream check) render in the modal's
  error banner, same as create today.

### 6.3 Data Layer (`web/src/lib/api/query-hooks.ts`)

```ts
export function useUpdateAuthProfile() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (req: Parameters<typeof authProfileClient.updateAuthProfile>[0]) =>
      await authProfileClient.updateAuthProfile(req),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: queryKeys.authProfiles });
    },
  });
}
```

## 7. Security & Trust Boundaries

| Concern | Mitigation |
| :--- | :--- |
| Secret exposure via read path | **None added.** Update returns the same read-only proto (`has_private_key` / `has_token` booleans only). No new decryption-for-response code path; `populateAppMetadata` usage is unchanged and never returns key material. |
| Secret exposure in UI | Secret inputs are always empty on open; placeholders instruct "leave blank to keep". The write-only model is preserved by construction. |
| Secrets in logs / audit | Audit entry records `{before: {name, auth_method}, after: {name, auth_method}, secrets_rotated: ["private_key"]}` — class names only, never values. Extends the existing `recordAuditLog` usage for create/delete. |
| Stale credential material | Method switches purge the old method's secret columns (NULL) in the same update. |
| Brute-force / validation oracle | Upstream credential validation runs **only when a new secret is supplied** — identical exposure to Create; renames cannot be used to probe provider APIs. |
| Encryption key rotation | Re-supplied secrets are encrypted with the *current* key (`db.Encrypt`), so routine rotation via edit also migrates ciphertexts forward. |
| Broken intermediate states | Read-merge-write keeps the row internally consistent (§4.2); a failed validation aborts before any write. |
| Authorization | Update is a mutating admin RPC — guarded by the same session-auth ConnectRPC interceptor chain as Create/Delete; no allowlist exceptions. |
| Binary transport | Frontend uses the ConnectRPC binary (`application/proto`) transport exclusively, as mandated; JSON transport remains disabled. |
| Leakage tests | `internal/server/leakage_test.go` gains an Update case asserting no secret material appears in responses, errors, or audit metadata. |

## 8. Test Plan

- **Go unit (`internal/server/auth_profile_test.go`, new cases):**
  rename success (+`updated_at` bump, audit entry written); rename to duplicate name →
  `CodeAlreadyExists`; unknown id → `CodeNotFound`; rotate token keeps other fields;
  blank secret keeps existing ciphertext (assert ciphertext unchanged); method switch
  requires new secret and purges the old column; invalid payloads → `CodeInvalidArgument`;
  validator invoked only when a secret is supplied.
- **Leakage suite:** Update response/audit metadata free of secret material.
- **Frontend (`web/src/routes/profiles.test.tsx`):** edit modal prefills metadata;
  blank secrets submit empty; method change marks secret fields required; success
  closes modal and invalidates queries; server error renders banner.
- **Manual/E2E:** rotate a PAT referenced by a running pool → pool keeps scheduling
  runners; rename profile → pool detail page reflects new name.

## 9. Implementation Checklist

1. Proto: add `UpdateAuthProfile` RPC + messages; `make proto` / `buf generate`.
2. DB: extend `AuthProfileDatabase` interface (no SQL changes).
3. Server: shared request validator, `UpdateAuthProfile` handler (merge semantics,
   purge-on-switch, audit), `CodeAlreadyExists` mapping; unit + leakage tests.
4. Web: `useUpdateAuthProfile` hook; `AuthProfileForm` refactor; Edit modal; tests.
5. Docs: this document; `docs/08` proto snippet; `docs/09` §4.8 mockup; README
   Features/Roadmap swap at implementation time.
