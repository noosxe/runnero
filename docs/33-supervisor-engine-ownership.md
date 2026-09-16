# Supervisor Engine Ownership

Preventing one supervisor instance from destroying another instance's runners
on a shared Docker engine.

- **Status:** Design phase (docs-only PR; implementation follows after review)
- **Tracking:** RUN-233
- **Depends on:** docs/03 (labels, architecture), docs/22 §5.4 (removed-pool
  detection, RUN-126), docs/25 (runner lifecycle: drain, backstop), docs/20
  §4.1 (ghost sweep)
- **New schema:** migration 010 (`pool_tombstones`)

## 1. Problem statement

The orchestrator assumes it is the **only** supervisor managing a Docker
engine. Two code paths make that assumption load-bearing:

1. **Boot adoption** (auditor `RebuildState`, docs/03 §2): on boot the
   supervisor scans the engine for containers labeled
   `com.runnero.managed=true` and adopts them into its tracker, keyed by the
   `com.runnero.pool-id` label — regardless of whether its own database knows
   the pool.
2. **Removed-pool drain** (reconcile step 5, docs/22 §5.4): any tracked pool
   id absent from the `runner_pools` table is treated as "pool was deleted",
   and its idle runners are gracefully drained every audit cycle (docs/25
   §4.2), with the busy-runner drain backstop (docs/25 §4.5) covering the rest.

Put together, a second supervisor process sharing the engine with an empty or
different database adopts the first instance's runners, finds their pool ids
missing from its own `runner_pools`, and drains them — every audit cycle, as
fast as the primary respawns them. The primary supervisor behaves exactly as
designed the whole time: it sees die events, records reap removals, and
maintains its targets.

This is not hypothetical. On 2026-09-16 a dev supervisor instance was
accidentally left running against the host engine with a scratch data
directory. It drained the production pool every ~10 seconds for 40 minutes
(249 runner removals, ~6 containers/minute of churn) and killed a CI E2E job
by destroying its runner mid-run. Forensics were slow because the victim's
logs show no termination — the killer was a different process, and nothing on
the containers identifies who spawned them.

## 2. Goals and non-goals

**Goals**

- G1: A supervisor must never stop, deregister, or drain a runner container
  unless it has positive evidence that it owns (or owned) that runner.
- G2: The documented convergence behaviors must keep working unchanged:
  restart adoption of own runners (docs/23), restart-after-pool-deletion
  drain (docs/22 §5.4), explicit `DeletePool` hard drain (docs/25 §4.2), and
  the drain backstop (docs/25 §4.5).
- G3: Collisions must be *loud*: a supervisor that finds runners it does not
  own logs an actionable warning naming them and the expected owner, instead
  of silently killing or silently ignoring.
- G4: Forensics: given a container, an operator can tell which supervisor
  instance spawned it.

**Non-goals**

- Protecting runners from hostile processes holding the Docker socket. The
  socket is root-equivalent by design (docs/03); labels are metadata, not an
  enforcement boundary against third parties. This design removes the
  *self-inflicted* destroyer class only.
- Supporting multiple cooperating supervisors on one engine (multi-instance
  management). If that becomes a product need, it gets its own design; this
  design only makes accidental collisions harmless and visible.
- Changes to runner-side auth or registration.

## 3. Design: ownership evidence for destructive actions

The core rule:

> Destructive host actions (container stop/remove, forge deregistration) are
> permitted only on runners the supervisor can prove it owns. Ownership
> evidence is: (a) the pool exists in the local `runner_pools` table, or
> (b) the pool has a local tombstone (it existed here and was deleted), or
> (c) the action is an explicit API call carrying the pool id
> (`DeletePool`), which by construction operates on a local pool.

Evidence is **local-state-based**, not container-label-based: a label can be
copied, and a fresh database must be able to distinguish "foreign" from
"mine-before-a-crash" — only local history can.

### 3.1 Pool tombstones (migration 010)

Pool rows are hard-deleted today. To keep evidence after deletion while the
supervisor is down or restarted, deletions leave a tombstone:

```sql
CREATE TABLE pool_tombstones (
    pool_id    INTEGER PRIMARY KEY,   -- the deleted runner_pools.id
    pool_name  TEXT NOT NULL,
    deleted_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

- `DeletePool` (and any pool delete path) inserts a tombstone in the same
  transaction that deletes the `runner_pools` row.
- Tombstones accumulate trivially (one row per pool ever deleted); no expiry
  needed. They may be pruned manually.
- Existing installations start with an empty tombstone table — this is safe
  because a currently-managed pool is evidence (a) regardless of tombstones.

### 3.2 Removed-pool drain gating

The reconcile step-5 loop (docs/22 §5.4) changes from
*"tracked pool missing from DB → drain"* to:

| Tracked pool | In `runner_pools` | Tombstone | Behavior |
|---|---|---|---|
| yes | yes | — | normal management (unchanged) |
| yes | no | **yes** | removed-pool graceful drain, unchanged (restart-after-delete convergence, docs/25 §4.2) |
| yes | no | **no** | **foreign pool: skip.** Log a warning listing the pool id/name and the adopted runner names; expose a foreign-runner gauge; never stop or deregister |
| no | yes | — | nothing tracked; reconcile spawns to target (unchanged) |

The middle-foreign row is the incident: a second instance adopts pool ids it
never had, finds no tombstone, and skips. The legitimate restart case is
preserved because a pool deleted in our own DB always leaves a tombstone.

Backstop behavior for a *draining* pool (docs/25 §4.4/§4.5) is unaffected:
once a pool qualifies for draining (explicit delete or tombstone), the
existing busy-runner backstop machinery runs as documented.

### 3.3 Ghost sweep

The ghost sweep (docs/20 §4.1) deregisters forge runners that are offline and
locally untracked. It already only iterates pools present in the local DB —
a foreign instance has no credentials for pools it does not own, so
cross-instance deregistration is structurally impossible there. No change
needed; this design documents the invariant so it survives refactors.

### 3.4 Instance identity and the owner label

Each supervisor gets a persistent **instance id** (UUID v4) generated on
first boot and stored in `app_settings` (`instance_id`). It is stamped onto
every spawned container as `com.runnero.owner=<instance-id>` alongside the
existing labels (docs/03 §2).

In this design the label is **informational only** — forensics (G4) and
log/metric enrichment ("skipping 1 foreign runner owned by
`9f2e…`"). It is deliberately *not* the ownership check: a copied database
would copy the instance id with it, and labels alone cannot distinguish
"spawned by another instance of me" from "spawned by a stranger". The
tombstone rule carries the security weight; the label carries the story.

### 3.5 Boot behavior and the escape hatch

At boot, after `RebuildState` adoption, the controller classifies every
adopted runner using the §3.2 matrix. Any foreign population produces:

- a `WARN` per foreign pool: pool id/name, runner names, owner label if
  present, and remediation guidance;
- a gauge/counter (`orchestrator_foreign_runners`) so the state is visible in
  metrics, not just logs.

**Escape hatch — claiming orphans.** A legitimately fresh database (wiped
data dir, restored-from-backup volume, moved engine) may face real orphans:
runnero containers with no owner anywhere. By design the supervisor will not
touch them; they would sit until manually removed. One-shot remediation:

```
SUPERVISOR_ENGINE_OWNERSHIP=adopt-all   # default: strict
```

With `adopt-all`, the boot adoption treats every `com.runnero.managed`
container as owned (today's behavior) and logs a prominent notice. The flag
is read at boot only, is intended for one-shot recovery, and is documented in
`.env.example` with a warning. `strict` is the default so the safe behavior
is what happens by accident.

### 3.6 What stays intentionally dangerous

Two supervisors sharing **the same database volume** still fight (both hold
the tombstone evidence). This is user error of a different class — two
processes writing one SQLite database — and SQLite locking makes it
unhealthy long before the drain question matters. The §3.4 owner label plus
the foreign-runner warning make the mistake visible in minutes instead of an
archaeology project. A hard engine-level leader lock was considered and
rejected (§7).

## 4. Data model and config changes

- **Migration 010:** `pool_tombstones` table (§3.1).
- **`app_settings`:** new `instance_id` key (UUID v4), set on first boot.
- **Container labels:** new `com.runnero.owner` (docs/03 §2 label table
  updated).
- **Config:** `SUPERVISOR_ENGINE_OWNERSHIP` = `strict` (default) |
  `adopt-all` (§3.5). Config plumbing per docs/06.
- **Metrics:** `orchestrator_foreign_runners` gauge; foreign-pool skip events
  logged at WARN.

## 5. Security implications

- The change *reduces* destructive blast radius: an operator mistake (second
  dev instance, wrong data dir) stops being able to destroy production
  runners.
- It does **not** defend against processes that hold the Docker socket and
  act maliciously; socket holders can always stop containers directly. The
  socket remains root-equivalent (docs/03 security note).
- The owner label must never be treated as authorization in future work;
  this doc fixes its status as metadata (§3.4).
- `adopt-all` is a deliberate, operator-chosen weakening; its use is logged
  at boot.

## 6. Test plan

- Unit: reconcile step-5 gating per §3.2 matrix row (mock engine + in-memory
  tracker; tombstone present/absent; `adopt-all`).
- Unit: `DeletePool` writes the tombstone transactionally; the hard-drain
  path is unchanged.
- Unit: boot adoption classification — own / foreign / adopt-all; warning
  content and gauge.
- Unit: first-boot `instance_id` generation and persistence across reopen.
- Regression: the §3.2 foreign row must not regress to draining (this is the
  incident, encoded as a test).

## 7. Alternatives considered

- **Engine leader lock** (second supervisor refuses to boot while a peer is
  live): strongest guarantee, but requires a lease/heartbeat mechanism to
  survive crashes, blocks legitimate parallel dev testing against a shared
  engine, and turns a soft conflict into a startup failure operators must
  debug. Rejected for v1; revisit if multi-instance coordination becomes a
  product need.
- **Per-boot spawn id label as the ownership check** (`spawned-by=<boot
  uuid>`): breaks restart adoption — after a crash the new boot would see its
  own predecessor's runners as foreign and refuse to adopt them, defeating
  docs/23. Rejected.
- **Config pool allowlist** ("this supervisor manages pools X,Y only"):
  config drift burden, no protection when the allowlist is wrong, and
  duplicates what the database already expresses. Rejected.

## 8. Open questions (owner)

- **OQ-1:** Escape-hatch shape: is `SUPERVISOR_ENGINE_OWNERSHIP=adopt-all`
  (§3.5) right, or do you prefer an explicit one-shot CLI subcommand (e.g.
  `runnero-supervisor adopt-orphans`) that exits after claiming?
- **OQ-2:** Foreign-pool discovery at boot: WARN (proposed) or ERROR?
  ERROR would page louder in log-based alerting but fires on every boot while
  a foreign population exists.
- **OQ-3:** Tombstone pruning: keep forever (proposed — rows are tiny) or add
  a retention sweep?
