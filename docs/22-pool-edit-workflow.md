# 22. Runner Pool Edit Workflow Design

## 1. Problem Statement & Motivation

Runner pools carry the full runner-orchestration contract: targets, registration
scope, labels, image, resource quotas, warm-pool sizing, and lifetime caps.
Today a pool's configuration is frozen at creation time: the wizard
(`web/src/components/pools/create-pool-wizard-modal.tsx`) creates, the detail
page's Config tab displays read-only, and the only mutation the UI exposes is
the Renovate sub-config (pool detail → Renovate tab, which quietly round-trips
the *entire* pool through `useUpdatePool`).

This forces operators into destructive workarounds for routine lifecycle
operations:

| Scenario | Current workaround | Pain |
| :--- | :--- | :--- |
| Raise/lower `min_idle` or `max_concurrency` | Delete + recreate pool | Kills all runners, including busy ones (`drainPool` terminates everything); loses pool wiring, diagnostics history, and Renovate config. |
| Fix a typo in labels / add a target repo | Delete + recreate | Same as above; job history detaches from live pool context. |
| Bump runner image or CPU/memory quota | Delete + recreate | Downtime for a pure config bump that the controller could converge without touching busy runners. |
| Rotate to a different auth profile (same provider family) | Delete + recreate | Pool identity churn for a credential concern; docs/17 already established that credential lifecycle must not require teardown. |
| Rename a pool | Not possible at all | Misnamed pools live forever. |

Unlike docs/17 (auth profiles), the RPC surface already exists:
`UpdatePool(UpdatePoolRequest{Pool}) → UpdatePoolResponse{Pool}` is defined in
`proto/api.proto`, implemented in `internal/server/pool.go`, backed by the
sqlc query `UpdateRunnerPool`, and consumed by `web/src/lib/api/query-hooks.ts`
(`useUpdatePool`). **What is missing is (a) safe edit semantics against live
runners, (b) backend hardening of the existing handler, and (c) any user-facing
edit UI.** This design supplies all three.

## 2. Goals & Non-Goals

### Goals

1. Edit **every pool configuration field** from the UI: name, auth profile,
   scope, target URLs, labels, runner image, `allow_docker`, `min_idle_runners`,
   `max_concurrency`, `cpu_limit`, `memory_limit`, `max_runner_lifetime_seconds`,
   and Renovate settings.
2. **Never disrupt running jobs.** Busy runners are one-job ephemeral
   (docs/04); edits converge the fleet by recycling *idle* runners only.
3. Make **rename** possible without the controller mistaking it for a pool
   deletion (which today force-terminates all runners of the old name,
   `internal/orchestrator/controller.go` §Reconcile step 5).
4. Harden `UpdatePool`: correct error codes (duplicate name →
   `CodeAlreadyExists`), no swallowed write errors, before/after audit
   metadata, no gratuitous `pool_targets` churn.
5. Reuse the create wizard as a shared create/edit component so both modes
   stay in lockstep — mirroring the `AuthProfileModal` precedent (docs/17 §6).

### Non-Goals

- **Field-mask / partial updates (`update_mask`):** the Pool message carries no
  write-only secrets; the edit form is always prefilled with the full current
  state, so full-replace semantics (same as Create) remain correct and simpler.
- **Optimistic concurrency (ETag / version column):** consistent with the
  docs/17 decision — SQLite is a single-writer store; last-write-wins with an
  authoritative `updated_at`.
- **Editing `provider`:** provider is deduced from the auth profile family at
  create time and is **immutable after creation** (§5.3). Cross-provider
  migration (e.g. a forge migrating Gitea → Forgejo) is a re-create operation;
  see §11 follow-ups.
- **Editing runtime/read-only fields** (`active_runners`, `idle_runners`,
  `health_status`, diagnostics, `image_update_available`, `latest_image`):
  the server ignores them on input and rebuilds the response from the DB row
  via `toProto` — unchanged.
- **Drift detection / reconfiguration of busy runners:** busy runners keep
  their spawn-time config and exit after their job (ephemeral model); no
  in-place reconfiguration is attempted.
- **Controller tracking refactor (name-keyed → ID-keyed):** the structural fix
  that would make renames race-free by construction; deliberately out of scope
  (§11).

## 3. Requirements & Invariants

### 3.1 Functional Requirements

1. `UpdatePool` remains the single mutation RPC; **no proto changes** (§5.1).
2. Editing is offered from (a) the pools list page per pool card and (b) the
   pool detail Config tab.
3. Edits that change runner **spawn identity** (§5.2) recycle the pool's idle
   runners; busy runners are never terminated by an edit.
4. Rename requires **zero busy runners** at commit time; otherwise
   `CodeFailedPrecondition` with an actionable message.
5. Duplicate pool name → `CodeAlreadyExists` (today: unhandled
   `UNIQUE(name)` violation surfacing as `CodeInternal`).
6. Unknown pool id → `CodeNotFound`; invalid payload → `CodeInvalidArgument`;
   referencing a missing auth profile → `CodeInvalidArgument` (existing FK
   mapping).
7. Every successful update writes a `pool.update` audit entry with
   before/after metadata **restricted to changed fields**.
8. The Renovate tab keeps working unchanged (it already sends a full Pool
   message; hardened handler semantics are transparent to it).

### 3.2 Invariants Preserved

- Pools always reference an existing auth profile (FK) and a provider from the
  allowed set; Gitea/Forgejo pools keep `allow_docker=true` (docs/05 §4).
- Target homogeneity per scope (all-repos or all-orgs) is re-validated on
  every update exactly as on create (`validatePoolInput`).
- Pool rows carry no secret material; auth credentials live exclusively in
  auth profiles (docs/05, docs/17).
- Audit entries and RPC responses never contain runner registration tokens
  (minted per-spawn, never persisted in pool config).

## 4. Current State Audit (as of this design)

| Layer | State | Gap |
| :--- | :--- | :--- |
| `proto/api.proto` | `UpdatePool` defined, full-Pool replace shape | none |
| `internal/db/queries/runner_pools.sql` | `UpdateRunnerPool :one` full-row `UPDATE … RETURNING *`, bumps `updated_at` | none |
| `internal/server/pool.go` `UpdatePool` | Implemented: validates, updates row, upserts renovate config, rewrites `pool_targets`, audits, `statsProvider.Reload` | Duplicate name → `CodeInternal` (UNIQUE unhandled); renovate cron re-validated *after* the row update (dead duplicate of `validatePoolInput`); renovate create-fallback and all target writes swallow errors (`_, _ =`); targets rewritten on **every** save even when unchanged (the Renovate tab triggers this weekly); audit metadata covers only 4 fields, no before/after; no runner-impact handling at all (rename → controller drains old name, killing busy runners) |
| `internal/orchestrator/controller.go` | `Reconcile` converges `min_idle`/`max_concurrency` after `Reload`; removed-name detection drains; runners capture pool config **at spawn time** (`spawnSingleRunner`) | No rename awareness; no idle-recycle entry point; stale-config idle runners satisfy warm-pool accounting indefinitely (until lifetime expiry) |
| `web/src/lib/api/query-hooks.ts` | `useUpdatePool` exists, invalidates `queryKeys.pools` (prefix-cascades to pool detail + runners) | none needed |
| `web/src` routes/components | Renovate tab is the sole consumer; pools list and Config tab are read-only | entire edit UI |

## 5. Edit Semantics

### 5.1 Protocol — Unchanged

`UpdatePool` keeps its full-replace shape. The request Pool must carry the
`id` plus every writable field (the UI prefills from the live object, so
nothing can be accidentally blanked); server-side read-only fields in the
request are ignored and the response is rebuilt from the persisted row.

### 5.2 Field Classes & Runner Impact

| Field | Class | Effect of edit |
| :--- | :--- | :--- |
| `min_idle_runners`, `max_concurrency`, `max_runner_lifetime_seconds` | **Control** | Converges on next reconcile tick (docs/03 §4) — no runner interaction. Lifetime is busy-anchored (docs/23): a lowered limit can terminate in-flight busy runners on the next tick; idle standbys are unaffected. |
| Renovate `enabled` / `cron_schedule` / `image` | **Control** | Affects the Renovate scheduler only. |
| `name` | **Control** | Metadata-only rename — no runner interaction (§5.4, as amended by RUN-126). |
| `auth_profile_id` | **Spawn identity** | Idle runners are registered under the old profile; recycle so respawns mint tokens via the new profile. (Secret *rotation inside* a profile already propagates to future spawns — docs/17.) |
| `repository_url` / `target_urls`, `scope` | **Spawn identity** | Idle runners are registered against old targets; recycle. |
| `labels`, `runner_image`, `allow_docker`, `cpu_limit`, `memory_limit` | **Spawn identity** | Baked into the container at spawn; recycle idle so the warm pool reflects the edit immediately instead of at lifetime expiry. |
| `provider` | **Immutable** | Rejected with `CodeInvalidArgument` (§5.3). |

**Rule:** if any spawn-identity field changed, the server asks the controller
to recycle the pool's idle runners *after* the update transaction commits
(§6.2; the write itself is transactional since RUN-129). Busy runners
finish their job and exit — natural convergence, zero job disruption.
Recycling only ever follows a successful write: a failed update leaves the
pool untouched and skips the recycle churn entirely.

### 5.3 Provider Immutability

Provider is a product of the auth-profile family chosen at creation; switching
it retroactively also implies a target-set belonging to another provider and a
different registration API. Rather than validating that cross-product, a pool
that needs a different provider is recreated. The edit UI locks the auth
profile selector to profiles of the pool's current family, making the rule
visible instead of error-driven.

### 5.4 Rename Semantics

*As implemented (RUN-126 amendment):* the original design keyed tracked
runners, diagnostics, and the provisioning queue by **pool name**, making a
rename indistinguishable from delete+create and forcing a busy-runner
precondition plus an idle recycle. The shipped implementation keys all
controller-side state by **pool database id**:

1. Renames are metadata-only: the reconciler, diagnostics, ghost counters,
   and provisioning queue never observe the change, so no busy-guard and no
   recycle are needed.
2. Spawned containers keep their spawn-time `pool-name` label (and container
   name) until they recycle naturally; API responses fill the pool name from
   the database row, so the UI is never stale.
3. Deleted pools are detected by id diff; a renamed pool keeps its id and is
   therefore never drained.

The busy-guard race called out below is eliminated by construction.

<details>
<summary>Original design (pre-RUN-126), kept for context</summary>

The reconciler keys tracked runners by **pool name**. A naive rename is
indistinguishable from delete+create, and the delete path (`drainPool`)
deregisters and terminates *all* tracked runners — including busy ones.
Therefore:

1. Precondition: `PoolStats(poolName).active == 0` (no busy runners).
2. Idle runners are recycled so nothing worth keeping remains tracked under
   the old name.
3. The DB rename then leaves the old name with an empty tracked set.

**Known narrow race:** a job can land on an idle runner between the
busy-check and the commit; that runner is then terminated by the
removed-name drain on the next reconcile. Accepted; the structural fix is
ID-keyed tracking (§11, RUN-126).

</details>

### 5.5 Ordering (single logical flow)

```text
1. validatePoolInput(req.pool)                // shape, targets, scope, cron, provider immutability
2. GetRunnerPoolById(id)                      → CodeNotFound; capture "before"
3. if renamed: busy-check (PoolStats)         → CodeFailedPrecondition
4. if spawn-identity changed OR renamed:
       RecycleIdleRunners(oldName)            // best-effort, logged; no-op when no controller
5. UpdateRunnerPool / renovate upsert / conditional target rewrite
6. recordAuditLog("pool.update", before→after changed fields)
7. statsProvider.Reload(ctx)                  // converge min_idle/max_concurrency, respawn recycled idle
```

## 6. Backend Changes

### 6.1 Handler Hardening (`internal/server/pool.go`)

- Map `db.IsUniqueConstraintError` on `UpdateRunnerPool` to
  `CodeAlreadyExists` (helper + pattern already shipped with docs/17).
- Delete the post-write renovate cron re-validation (dead duplicate; all
  validation happens in step 1).
- Stop swallowing errors: renovate create-fallback and `pool_targets`
  delete/insert failures return `CodeInternal` instead of `_, _ =`.
- Rewrite `pool_targets` **only when the normalized target set changed**
  (order-insensitive comparison against `existing` targets) — the Renovate
  tab's full-pool round-trips must not churn target rows.
- Audit metadata: `{before: {…}, after: {…}}` restricted to changed fields
  (labels list, target list, numeric quotas, booleans; no secret material
  exists in pool config by construction).
- Provider immutability check early in `UpdatePool` (request provider vs.
  `existing.Provider`, normalized).

### 6.2 Controller Hook (idle recycle)

One new optional capability, discovered by interface assertion on the existing
`statsProvider`, so absent controllers (unit fakes, web-only operation) degrade
to a logged no-op:

```go
// Implemented by orchestrator.PoolController; consumed by PoolService.
type IdleRecycler interface {
    // RecycleIdleRunners deregisters and terminates all non-busy tracked
    // runners of poolName so the next reconcile respawns them with the
    // pool's current configuration. Busy runners are never touched.
    RecycleIdleRunners(ctx context.Context, poolName string) error
}
```

`PoolController.RecycleIdleRunners` iterates `TrackedPoolRunners(poolName)`,
skips `IsBusy`, and for each idle runner runs the same
deregister → terminate → untrack sequence as `drainPool`, holding
`provisionMu` for the sweep. No schema, queue, or state-machine changes.

### 6.3 Data Access

No schema migration, no new SQL. `UpdateRunnerPool` already updates every
writable column and returns the merged row.

## 7. Frontend UI / UX

### 7.1 Entry Points

1. **Pools list** (`web/src/routes/pools.tsx`): pool card footer gains an
   **Edit** action (pencil icon) next to the existing detail link.
2. **Pool detail → Config tab** (`web/src/routes/pool-detail.tsx`): the
   "Pool Parameters & Resource Limits" card header gains an
   **Edit Configuration** button.

### 7.2 Shared Wizard (`web/src/components/pools/`)

`create-pool-wizard-modal.tsx` is generalized into
`pool-wizard-modal.tsx` with `mode: "create" | "edit"` and
`pool?: Pool` (edit prefills every step: name, auth profile, scope, targets,
labels, image, `allow_docker`, quotas, lifetime, Renovate). Behavioral deltas
in edit mode:

- **Step 1 (Identity & Auth):** name editable (slug validation unchanged);
  auth profile selector **filtered to the pool's provider family**
  (provider immutability, §5.3) with a helper line explaining the lock.
- **Step 2 (Scope & Discovery):** full multi-target editing incl. discovery
  against the selected profile; homogeneity validation unchanged.
- **Step 4 (Review):** retitled **Review & Save**; renders a
  **changed-fields diff** (old → new per field) instead of a plain summary.
- **Impact banners** (computed client-side from the field classes in §5.2):
  - spawn-identity fields changed → *"N idle runner(s) will be recycled to
    apply the new configuration; running jobs are not affected."*
  - name changed → *"Renaming recycles idle runners and requires no busy
    runners."*
- **Submit:** `useUpdatePool` with the full Pool message (id + all writable
  fields). `CodeFailedPrecondition` (busy runner appeared during rename) and
  `CodeAlreadyExists` (duplicate name) render as actionable banner text.
  Success closes the modal; existing `queryKeys.pools` invalidation
  prefix-cascades to detail, runners, and stats queries.
- The Renovate tab remains the shortcut for renovate-only edits and benefits
  transparently from the hardened handler (notably: no more target churn).

## 8. Security & Trust Boundaries

| Concern | Mitigation |
| :--- | :--- |
| Authorization | Update is a mutating admin RPC behind the same session-auth ConnectRPC interceptor chain as Create/Delete; no new route, no transport change (binary proto only). |
| Secret exposure | None added: pool config contains no credentials (auth is by profile ID); registration tokens are minted per spawn and never stored in the pool row. Audit diff records field names/values only — labels, quotas, URLs. |
| Destructive edits | Idle recycle never touches busy runners; rename is busy-guarded; provider switch rejected. The UI states impact before submit (§7.2 banners). |
| Target injection | Re-validation on every update via `validatePoolInput`: URL parsing, scope homogeneity, provider/`allow_docker` coupling (docs/05 §4). |
| Privilege drift via auth profile | Repointing `auth_profile_id` recycles idle runners so no stale registration outlives the edit; selector is family-locked. |
| Audit integrity | Before/after changed-field metadata replaces the current 4-field snapshot; delete/create pairs no longer masquerade as edits. |
| Resource exhaustion | No new upstream call paths in edit (discovery reuse is rate-limited per docs/14 §3.2); recycle is bounded by the pool's tracked runner set. |

## 9. Test Plan

- **Go unit (`internal/server/pool_test.go`, new cases):** rename success
  (audit + idle recycle + reload ordering); rename with busy runners →
  `CodeFailedPrecondition`; duplicate name → `CodeAlreadyExists`; unknown id →
  `CodeNotFound`; provider change → `CodeInvalidArgument`; spawn-identity
  change triggers `RecycleIdleRunners`, control-field change does not;
  unchanged target set → no target rewrite; invalid cron/URL/scope →
  `CodeInvalidArgument` **before** any write (assert no row touched).
- **Controller unit (`internal/orchestrator/`):** `RecycleIdleRunners`
  deregisters/terminates only non-busy runners and untracks them; busy runners
  untouched; reconcile afterwards respawns to `min_idle`; empty/unknown pool
  name is a no-op.
- **Frontend (Vitest):** edit modal prefills all steps from `pool`; auth
  selector family-locked; review step shows diff; spawn-identity change shows
  recycle banner; rename shows rename banner; submit sends full Pool with id;
  `CodeAlreadyExists` / `CodeFailedPrecondition` render banners; success closes
  modal and invalidates.
- **E2E (Playwright, `tests/e2e/`):** edit `min_idle` on a live pool → count
  converges; edit labels → idle runners recycled and respawned (observable in
  the runners tab), busy job unaffected; rename flow incl. busy-guard error
  path. Exercised via the E2E suite before the PR body's human-check list.

## 10. Implementation Checklist

1. Server: harden `UpdatePool` (§6.1) + `IdleRecycler` assertion & invocation
   (§6.2); unit tests.
2. Controller: `RecycleIdleRunners` (§6.2); unit tests.
3. Web: wizard generalization to `pool-wizard-modal.tsx`, edit entry points
   (§7.1), diff review + banners (§7.2); Vitest tests.
4. E2E: scenarios above.
5. Docs: `docs/08` UpdatePool semantics note; `docs/09` §6.2 expanded with the
   edit workflow + entry points; README Roadmap → Features swap.

## 11. Related Follow-ups (Linear, out of scope here)

- ~~**ID-keyed runner tracking** in the controller (structural fix making
  renames race-free by construction; also shrinks the rename busy-guard).~~
  *Implemented (RUN-126): tracking, diagnostics, ghost counters, and the
  provisioning queue key on the pool database id; renames are metadata-only.*
- ~~**`drainPool` busy-preservation** on pool delete (today it terminates busy
  runners unconditionally; a "drain gracefully" option deserves its own
  design).~~ *Designed (docs/25) and implemented (RUN-127): `DeletePool` gains
  `drain_graceful`; idle runners are removed immediately while busy runners
  finish their jobs under a lifetime backstop.*
- **Gitea → Forgejo forge-migration flow** (provider switch with target host
  validation), should the scenario materialize.
- ~~**Transactional pool+renovate+targets writes** (single sqlite tx).~~
  *Implemented (RUN-129): `db.UpdatePool` wraps the pool row, renovate
  upsert, and target rewrite in one transaction, and idle recycling moved
  after the commit so a failed update does not churn runners.*
