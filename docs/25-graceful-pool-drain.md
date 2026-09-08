# 25 — Graceful Pool Drain (Busy-Runner Preservation on Delete)

| | |
| :--- | :--- |
| Status | Design Phase |
| Linear | [RUN-127](https://linear.app/runnero/issue/RUN-127) |
| Touches | `internal/orchestrator` (drain semantics, lifetime backstop, boot adoption), `internal/server` (DeletePool RPC), `proto` (DeletePoolRequest), `web` (delete affordance + confirm dialog — new surface), docs/03, docs/22 §11 |

## 1. Problem

Deleting a runner pool destroys every container it manages — **including busy runners executing jobs**. The delete path (`internal/server/pool.go` `DeletePool` → row removal → controller removed-pool detection at `controller.go:465` → `drainPool` at `controller.go:1843`) iterates all tracked runners with `State == "running"` and issues deregister + terminate + untrack for each, busy or not. A two-hour E2E suite killed at minute 90 loses everything after the checkpoint; the job's conclusion is `interrupted` at best.

The issue also asks for a confirm-in-UI flow surfacing N busy jobs. Investigation found the UI side is **greenfield**: `useDeletePool` exists in `web/src/lib/api/query-hooks.ts` but no component calls it — pool deletion is API-only today.

## 2. Current behavior, precisely

- **RPC**: `DeletePool(id)` deletes `pool_targets` then the pool row, audits `pool.delete`, reloads stats. No options, no runner-awareness (`server/pool.go:742`).
- **Controller**: the audit reconciler detects pools present in memory but absent from the DB and hard-drains them (`controller.go:465–485`): every `running` tracked runner is deregistered, terminated, untracked; provisioning-queue entries are purged; pool diagnostics removed.
- **Container labels**: containers carry `com.runnero.pool-id` / `pool-name` / `managed=true` labels (`orchestrator/docker/docker.go:241`), so the audit (`auditor.go` `Audit`) keeps listing and tracking them under the deleted pool's ID **even after the row is gone** — tracking is label-driven, not FK-driven. This is the property the design builds on.
- **Deregistration for deleted pools silently no-ops**: `deregisterRunner` (`controller.go:944`) resolves the provider by matching the runner's pool **name against DB pools**; a deleted pool matches nothing and the function returns. Lingering registrations are cleaned later by the ghost sweep (docs/20: idle + offline + untracked, 3 cycles).
- **Lifetime enforcement is blind to deleted pools**: `checkHungRunners` iterates DB pools only, so a hung busy runner of a deleted pool has **no kill switch** — it runs until the job process dies on its own.
- **Busy-state convergence is blind to deleted pools**: both busy-state inputs (webhook matching, per-pool busy-sync) key on existing DB pools. For a deleted pool the busy flag freezes — irrelevant today (everything is killed), load-bearing for this design (§4.4).
- **Ephemeral exit is the completion signal**: runnero runners run with `--ephemeral`; the runner process exits after its single job, the container exits, and the audit cycle reaps it (`report.Exited` → `reapContainer`, `controller.go:430`). Container exit does not require the pool row to exist.

## 3. Goals and non-goals

**Goals**

1. A per-delete choice: terminate everything now (today's semantics) or drain gracefully — busy runners finish their current job, then their containers are reaped; idle runners are cleaned up immediately in both modes.
2. No unbounded leftovers: drained busy runners keep a lifetime kill switch even though the pool row is gone.
3. Crash-safe: supervisor restart mid-drain converges to a safe state without durable drain bookkeeping.
4. The confirm flow surfaces how many runners are idle vs busy before the user commits, and makes the drain choice explicit.

**Non-goals**

- Pool-level persistent drain settings (this is a per-delete decision, not pool config).
- Pausing/quiescing *new* job dispatch to a draining pool (the pool registration is being torn down; GitHub stops dispatching to deregistered runners; in-flight registrations self-deregister at exit — §4.6).
- Migrating busy jobs to another pool.

## 4. Design

### 4.1 API: one optional flag on DeletePool

`DeletePoolRequest` gains `bool drain_graceful = 2` (field 1 stays `id`). Default `false` preserves today's hard-terminate behavior for every existing client. The audit log records the chosen mode in `pool.delete` metadata.

### 4.2 Controller: drain mode awareness

Today the server→controller boundary is implicit: the server only deletes the row and the controller notices on its next reconcile. The design makes drain intent explicit while keeping the row deletion as the single source of truth:

- New narrow interface on the controller (mirroring the `IdleRecycler` pattern from docs/22): `PoolDrainer { DrainPool(ctx, poolID int64, graceful bool) }`. `DeletePool` calls it after the row delete succeeds; the reconciler's removed-pool detection remains as the fallback for restarts and out-of-band deletes (see §4.5 — it degrades to graceful).
- **Hard mode (default, unchanged)**: current `drainPool` behavior verbatim — deregister/terminate/untrack every running runner, purge queue, drop diagnostics.
- **Graceful mode**:
  - Idle (tracked, running, not busy) runners: deregister → terminate → untrack, exactly like hard mode. They have nothing to preserve.
  - Busy runners: **left untouched** — no terminate, no deregister, no untrack. The container keeps running its job; when the job finishes, `--ephemeral` exits the runner process, the container exits, and the generic audit reap path removes it. Requiring nothing new, this reuses the strongest completion signal available.
  - Queue purge and diagnostics removal happen as in hard mode.
  - The pool ID is recorded in an in-memory **draining set** (`map[int64]drainEntry`). The set is metadata only — tracked runners stay under the dead pool ID because labels keep them auditable (§2).

### 4.3 Why no busy→idle detection is needed

For deleted pools, neither busy-state input works (§2). The design deliberately does not need them: the *completion* signal is container exit, not the busy→idle transition. The busy flag may read stale on a draining runner; it is never consulted for drain decisions after the initial idle/busy split. Job-history rows still close: the reap path closes rows on container death (docs/21 §5.2 "busy→idle **or container death** = job end"); implementation must verify the close path is pool-row-independent and fix if not.

### 4.4 Lifetime backstop

`checkHungRunners` gains a second loop over the draining set: for each still-running drained runner, force-terminate when `now − busyAnchor ≥ backstop`, using the docs/23 anchor rules (busy anchor from `BusySince`, spawn-clock fallback for adopted runners). Backstop per pool:

- `max_runner_lifetime_seconds` if the deleted pool had one set (the same guarantee the pool had in life),
- otherwise a fixed conservative cap — proposal: **6 hours** — so a pool deleted without a lifetime switch can never leak containers indefinitely (open question §8.3).

Ungraceful deaths (OOM, `docker kill`, host reboot) need no new handling: the container disappears, the audit `Disappeared` path untracks it, and the ghost sweep removes the stale registration (docs/20) — existing machinery, unchanged.

### 4.5 Restart and out-of-band deletes: graceful by construction

The draining set is in-memory and intentionally **not** persisted. If the supervisor restarts mid-drain, the boot audit adopts the dead pool's containers under their label pool ID (§2), removed-pool detection fires, and the design changes the fallback semantics: **removed-pool detection always drains gracefully** (idle → terminate now; busy → let finish, subject to §4.4 backstop). Consequences:

- Explicit user-chosen hard drains only apply within the live session; a restart degrades leftovers to graceful. Accepted: after a restart the user's "now" intent is unrecoverable, and graceful is the conservative choice for mid-job work.
- Deletes performed out-of-band (SQL) — unreachable by the RPC — also converge gracefully instead of killing busy jobs. Strictly better than today.

### 4.6 Registration cleanup without pool credentials

The supervisor cannot deregister a deleted pool's runners (`deregisterRunner` needs the pool's provider, §2) and must not resurrect credentials. Instead the design leans on the existing chain: the `--ephemeral` runner **self-deregisters** at job exit (its registration is removed by the runner process itself); reap removes the container; ghost sweep is the backstop for anything that died ungracefully. No new provider calls, no deleted-credential use — this is also why graceful drain adds no credential-handling surface.

### 4.7 Web: delete affordance and confirm dialog (new surface)

- **Entry point**: a destructive "Delete Pool" action on the pool detail page (danger zone), wired to the existing `useDeletePool`.
- **Confirm dialog** (replaces nothing — first delete UI) shows, from already-fetched pool runner data (existing runner list / pool state queries, no new RPC):
  - runner summary: **N idle · M busy**;
  - a radio choice: **"Drain gracefully"** — idle runners removed now, M busy runners finish their jobs (preselected when `M > 0`) — vs **"Terminate everything now"** (preselected when `M = 0`);
  - the lifetime-backstop promise in the drain description ("hung jobs are force-terminated after X"), so draining is bounded in UI language.
- The dialog wording must make the asymmetry clear: idle runners go immediately in both modes; only busy runners differ.

## 5. Protocol changes

- `supervisor.v1.DeletePoolRequest`: `int64 id = 1; bool drain_graceful = 2;` — additive, backward compatible; `false` (zero value) is today's behavior.
- `DeletePoolResponse` unchanged.

## 6. Security implications

- **No new credentials or provider calls.** Graceful drain performs strictly fewer provider operations than hard drain (zero deregistrations; relies on ephemeral self-dereg + ghost sweep).
- **Longer-lived containers are the cost of preservation.** A deleted pool's runner keeps its registration and job process alive for the remainder of the job. Exposure is bounded by the §4.4 backstop and unchanged container hardening (non-root, cap-drop). No secrets are involved beyond what the runner already holds to finish its current job.
- **Auditability**: `pool.delete` audit metadata gains `drain_graceful`; drain progress is visible via existing runner listings (draining runners remain label-auditable) and supervisor logs.

## 7. Testing plan (implementation PR)

Unit matrix (mock engine/reconciler, deterministic clock):

- hard mode = exact current behavior (regression lock);
- graceful: idle terminated immediately, busy untouched, queue + diagnostics purged in both;
- reap path removes exited drained runners and closes their job-history rows without the pool row;
- lifetime backstop: fires at pool lifetime / fixed cap; docs/23 anchor rules apply;
- restart adoption: dead-pool containers at boot drain gracefully; interrupted hard drain converges;
- RPC: flag pass-through, audit metadata, default-false compatibility;
- web: dialog counts, preselection matrix (`M>0` → drain, `M=0` → terminate), mutation payload.

No E2E changes planned; the E2E delete flows (if any) keep passing with the default flag.

## 8. Open questions

1. **API default** — keep hard-terminate as the wire default (recommended; backward compatible) or flip the default to graceful and make hard opt-in?
2. **Dialog preselection** — recommended: graceful preselected when busy runners exist, terminate otherwise. Alternative: always preselect terminate (consistent with "delete is destructive").
3. **Fixed backstop length** for pools without `max_runner_lifetime_seconds` — proposal 6h. Alternatives: 24h, or refuse graceful drain for lifetime-less pools (forces the user to pick terminate).
4. **Delete affordance scope** — pool detail page only (recommended) or also a row action on the pools list?

## 9. Implementation checklist

- `proto/api.proto` + codegen: `drain_graceful` field.
- `internal/server/pool.go`: pass-through, audit metadata; `PoolDrainer` wiring in daemon assembly.
- `internal/orchestrator`: `drainPool` split, draining set, `checkHungRunners` extension, removed-pool detection → graceful fallback, reap/job-history verification.
- `web`: delete button + confirm dialog + tests.
- Docs riding the implementation PR: docs/03 (§3/§4 drain semantics), docs/22 §11 follow-up struck like RUN-126's, this doc's Status → accepted & implemented, README Roadmap → Features.
