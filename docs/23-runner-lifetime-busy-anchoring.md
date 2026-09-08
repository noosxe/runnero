# 23. Runner Lifetime: Anchor `max_runner_lifetime` to First Busy Assignment

| | |
| :--- | :--- |
| Status | Accepted & implemented — design merged in PR #191, implemented by RUN-138 |
| Linear | RUN-122 (milestone M26) |
| Touches | `internal/orchestrator` (hung-runner check, busy-state transitions, scale-to-zero), docs/03, docs/21 |

## 1. Problem

The lifetime kill switch counts from **container spawn**. For pools running
idle standbys (`min_idle_runners > 0`), every standby is force-terminated
exactly `max_runner_lifetime_seconds` after coming up, then respawned ~1.5 s
later by the replenisher. Observed on pool `kraken-runners`
(`max_runner_lifetime_seconds = 7200`): one registration + container churned
every ~2 h all night (23:38, 01:39, 03:39, 05:39):

```
WARN hung runner exceeded max lifetime, force terminating  elapsed=7208.299s limit=7200s
```

The switch exists to reap **hung jobs**. Idle standbys never run jobs, so the
churn buys nothing and burns forge registrations, API quota, and container
cycles. Root cause: in `checkHungRunners` (`internal/orchestrator/controller.go`)
the elapsed clock is `now - r.SpawnedAt` for **every** running container,
regardless of busy state — the `IsBusy` flag only gates whether a `timeout`
job-history row is written (docs/21 §5.2).

## 2. Goals

- The lifetime clock starts at **first busy assignment** (job pickup), not spawn.
- Idle standbys are **never** lifetime-terminated.
- Busy/hung runners keep the force-terminate guarantee — never weaker than
  today's bound, and never weaker after a supervisor restart.
- Semantics fully specified: unassignment, busy-sync interaction, config
  validation, migration.
- No schema, proto, or UI changes.

## 3. Non-goals

- Bounding total idle-standby age (see §4.7 — recycling stays with the existing
  paths: scale-to-zero, pool edits, image updates, shutdown).
- Bounding total idle-standby age (see §8 — recycling stays with the existing
  paths: scale-to-zero, pool edits, image updates, shutdown).
- New config knobs. `max_runner_lifetime_seconds` keeps its name, type,
  default, and `0 = disabled` meaning.

## 4. Design

### 4.1 The busy anchor

Add one field to the in-memory runner state:

```go
// RunnerStatus (internal/orchestrator/provider.go)
BusySince time.Time `json:"busy_since,omitempty"`
```

`BusySince` is the anchor for the lifetime check: the moment this supervisor
lifetime first observed the runner busy. It is **ephemeral reconciler state**
(like `IsBusy`/`ForgeID`) — never persisted to SQLite, never sent over RPC.

### 4.2 Transition rules (who sets the anchor)

All busy-state transitions already funnel through
`Reconciler.MarkRunnerBusy(name, busy)` from exactly two signal sources
(docs/19): the `workflow_job` webhook fast path (`in_progress`/`completed`,
`webhook_scaling.go`) and the per-cycle remote listing
(`applyRemoteBusyState`, ~10 s audit interval). The rules:

| Transition | `BusySince` |
| :--- | :--- |
| idle → busy (webhook or busy-sync) | set to `now` **only if currently zero** (set-once) |
| busy → idle | **unchanged (sticky)** — see §4.4 |
| adoption/boot sync of an already-busy runner | seed with `SpawnedAt` (conservative, §4.4.1) |
| re-observation merging (`SyncState`) | preserve known `BusySince` over a zero listing value, same pattern as `SpawnedAt`/`IsBusy`/`OnDemand` today |
| container exit / untrack | state dropped with the runner |

**Set-once**: while a container lives, its anchor never moves forward. A
listing flap (docs/19 §2.4: worst case one audit cycle) that toggles
busy→idle→busy cannot extend a hung job's wall clock — the earliest anchor
wins. This is deliberate: for one-job ephemeral runners (docs/04) a live
container never legitimately starts a *second* job, so stickiness has no
downside and closes the flap loophole.

### 4.3 Hung-runner check rework

`checkHungRunners` becomes a busy-only check:

```go
for _, r := range tracked {
    if r.State != "running" || !r.IsBusy {
        continue // idle standbys are never lifetime-terminated
    }
    anchor := r.BusySince
    anchorSrc := "busy"
    if anchor.IsZero() {
        // Defensive: busy without anchor must not be unbounded (§4.4.1).
        anchor, anchorSrc = r.SpawnedAt, "spawn"
        if anchor.IsZero() {
            continue
        }
        c.logger.Warn("busy runner without busy anchor, falling back to spawn time", ...)
    }
    if now.Sub(anchor) > lifetimeLimit {
        // existing force-terminate path: capture logs, terminate,
        // untrack, record timeout row, drain queue
    }
}
```

- The `elapsed`/`limit` log line gains `anchor` and `anchor_source`
  (`busy` | `spawn`) so operators can see which clock fired.
- Termination is now *only* reachable for busy runners, so the existing
  `r.IsBusy` guard before `RecordJobTimeout` becomes always-true — kept as a
  defensive assert, and the docs/21 §5.2 caveat about idle standbys recording
  nothing becomes moot (harmless to keep).
- The docs/21 §5.4 belt-and-braces (`CloseStaleOpenJobs` at
  `2 × max_runner_lifetime_seconds`) is untouched: it is already anchored to
  the job row's `started_at`, not spawn.
- The timeout row's `started_at` currently passes `r.SpawnedAt`
  (`RecordJobTimeout(..., r.SpawnedAt, now)`); it now passes the same anchor
  used for the kill decision (`BusySince`, spawn fallback), so recorded job
  duration matches the enforced window instead of overstating it by the idle
  prefix.

### 4.4 Unassignment: the clock stays (sticky), with a spawn fallback

The issue asks explicitly: on unassignment, does the clock reset or clear?
**Neither — it clears only when the container's tracked state is dropped.**

Rationale: a busy→idle flip on a *still-running* container is anomalous for
one-job ephemeral runners — either the job finished and the container is about
to exit (the lifetime check no longer applies to it anyway, since it skips
non-busy runners), or it is a listing race (docs/19 §2.4). Resetting on
re-flip would let a hung job outlive its limit by one flap cycle. Clearing on
every unassignment would have the same effect via the next assignment.

#### 4.4.1 Unknown anchor ⇒ spawn (never weaker than today)

Two paths produce a busy runner with no anchor, and both fall back to
`SpawnedAt`:

1. **Adoption after restart / boot sync**: a runner already mid-job when this
   supervisor lifetime first sees it gets `BusySince = SpawnedAt` seeded at
   adoption. Its deadline is then exactly today's `spawn + L` — the restart
   case cannot outlive the old semantics.
2. **Defensive**: any busy runner with a zero anchor at check time falls back
   to `SpawnedAt` with a warning (should be unreachable given rule 1 and the
   transition rules; the guarantee must not depend on that belief).

Consequence for continuously tracked runners: the deadline moves from
`spawn + L` to `pickup + L`. Since `pickup ≥ spawn`, busy runners run **at
least** as long as before — the force-terminate guarantee is preserved
(hung jobs still die within `L` of pickup, or `L` of spawn worst-case), and
the small intentional extension (the idle prefix no longer eats into the
job's budget) is the point of the change.

### 4.5 Interaction with busy-sync (docs/19) and webhooks

- **Webhook path** (`workflow_job.in_progress`): anchor set at delivery,
  sub-second from actual pickup. Primary, most accurate source.
- **Busy-sync path** (missed webhook): anchor set at first busy observation,
  up to one audit interval (~10 s default) after actual pickup. The deadline
  extends by that same bounded skew — a job is never terminated before
  `L` of *observed* busy time. Acceptable at a 10 s convergence bound
  (docs/19 §2.4 rejects tighter synchronization for the same reason).
- The offline guard and absent-name guard (docs/19 §2.3) protect the anchor
  transitively: they prevent spurious idle flips, which is what keeps the
  sticky anchor from being relevant in steady state.

### 4.6 Scale-to-zero: drop the lifetime cap on idle grace

`reconcilePool`'s scale-to-zero branch currently caps the on-demand startup
grace at the lifetime (`gracePeriod = min(grace, lifetime)`,
`controller.go`). That cap existed to align the graceful drain with the
force-kill that followed it. Under busy-anchored semantics the lifetime never
applies to idle runners, so the cap is a category error and is **removed**:
orphaned on-demand runners that never pick up a job are governed by
`scale_to_zero_grace_period` alone. Behavior change is confined to pools
where `lifetime < grace` — those runners now wait the full grace period
before a *graceful drain* (no force-kill churn either way).

### 4.7 What still bounds an idle standby

Idle standbys become potentially long-lived by design (that is the fix).
Existing recycling paths are unchanged and remain the answer:

- Scale-to-zero pools (`min_idle_runners = 0`): idle runners drained
  per §4.6.
- Fixed idle target: excess above target drained (RUN-42); target-level
  standbys persist until recycled below.
- Pool edits (docs/22): spawn-identity edits recycle idle runners.
- Image updates (docs/03 §6) and graceful shutdown (docs/03 §7).

## 5. Config validation

No new constraints. `max_runner_lifetime_seconds` keeps: integer seconds,
`> 0` enables, `<= 0` disables the kill switch, no upper bound. Its *meaning*
changes to "maximum busy wall-clock per job assignment, measured from first
observed pickup". Field descriptions in docs/02 §config, docs/07, docs/08,
and the UI are reworded in the implementation PR to say so (busy-anchored).
Operators who relied on lifetime as a crude idle-fleet recycler must move to
the paths in §4.7 — called out in the migration notes.

## 6. Backward compatibility & migration

- **No schema/proto/UI migration.** `BusySince` is in-memory only.
- **Unchanged configs, busy runners**: force-terminated at
  `pickup + L` (≤ one audit cycle skew) instead of `spawn + L` — same
  guarantee, slightly more job time. Never terminated earlier than today.
- **Unchanged configs, idle runners**: the intentional change. Standby churn
  stops the moment the supervisor updates; existing standby containers are
  simply no longer killed at `spawn + L` (nothing to migrate — the next
  natural recycle absorbs them).
- **Supervisor restart mid-job**: adopted-busy runners keep exactly today's
  `spawn + L` bound (§4.4.1).
- **Interim mitigation** (pre-ship, from RUN-122): affected pools should
  clear or raise `max_runner_lifetime_seconds` (e.g. `86400`) to stop the
  churn now; after this ships, revert to the intended value.

## 7. Security implications

None adverse; strictly positive:

- The hung-job kill switch — the security-relevant guarantee (untrusted
  workflow code cannot run unbounded) — is preserved for every busy runner,
  including post-restart (§4.4.1) and listing-flap (§4.2) cases.
- Idle standbys execute no workflow code, so excluding them from the switch
  removes no control. Their eventual removal remains owned by the drain /
  recycle paths (§4.7), which are graceful (deregister + terminate), unlike
  the force-kill.
- No new inputs, no new privilege, no persistent state.

## 8. Testing plan (implementation PR)

Unit tests in `internal/orchestrator` (mock provider + reconciler harness,
existing patterns from `controller_test.go` / `busy_sync_test.go`):

1. Idle standby older than the limit is **not** terminated and keeps serving.
2. Busy runner terminated once `now - BusySince` exceeds the limit.
3. Busy runner terminated via spawn fallback when anchor is zero (adoption).
4. Adoption of an already-busy runner seeds `BusySince = SpawnedAt`.
5. Webhook `in_progress` sets the anchor; `completed` does not reset it
   (sticky across a flap: busy→idle→busy keeps the earliest anchor).
6. Busy-sync late detection: anchor = observation time, deadline skews by the
   observation delay, never shortens.
7. Scale-to-zero: orphaned on-demand idle runner drained at full grace
   period even when `lifetime < grace` (cap removed); busy runner never
   drained (existing regression test must keep passing).
8. Timeout row: `started_at` equals the anchor used for the kill (busy or
   spawn fallback), `status='timeout'`.
9. `max_runner_lifetime_seconds = 0`: no termination, busy or idle (existing
   behavior, regression-guarded).

## 9. Documentation updates (implementation PR)

- **docs/03** §4: "Hung Runner Auto-Termination" reworded to busy-anchored
  semantics (idle standbys excluded, anchor sources, spawn fallback); §7 note
  updated ("a *job* exceeding its lifetime…" — still force-killed regardless
  of shutdown state).
- **docs/21** §5.2: the idle-standby caveat becomes moot (lifetime kills are
  busy-only); reworded. §5.4 unchanged (already job-anchored).
- **docs/02** / **docs/07** / **docs/08**: field description reworded to
  "maximum busy wall-clock per job assignment".
- **docs/19**: short cross-reference note in §2.3 that the idle→busy
  transition now also stamps the lifetime anchor.
- **docs/22**: §5 field-class table row for `max_runner_lifetime_seconds`
  unchanged (still Control), wording tweak only.
- **README.md**: move from Roadmap *[Design Phase]* to Features on merge of
  the implementation PR.
