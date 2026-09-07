# Ghost Runner Sweep (docs/20)

| | |
| :--- | :--- |
| **Status** | Shipped — implemented on `feature/ghost-runner-sweep` (design approved via PR #178) |
| **Milestone** | Ghost Runner Sweep (M24) — RUN-114 → RUN-115 |
| **Related** | `docs/03` §7 (Graceful Shutdown Protocol), `docs/19` (Runner State Busy Sync — provides the listing), `docs/02` §3.2 (Provider Abstractions) |
| **Bug** | Orphaned GitHub runner registrations survive their containers and linger as `Offline` on the repo's Runners page |

## 1. Problem Statement

Runnero guarantees ephemeral runners: one job ↔ one container ↔ one registration. The guarantee is layered — `--ephemeral` self-deregistration, the entrypoint's `SIGTERM` trap, and the supervisor's API-driven `RunnerDeregistrar` — but ungraceful container deaths bypass **every** layer:

1. **Ungraceful container death** (agent crash, OOM kill, `docker kill`, host power loss, Docker daemon restart): the entrypoint trap only fires on `SIGINT`/`SIGTERM`, so `config.sh remove` never runs. The supervisor's cleanup for dead containers — the die-event path (`reapContainer`) and the auditor's `Disappeared` handling — removes the container and untracks it, but **never calls the provider deregistration API**. The registration is orphaned; GitHub marks it `Offline` on lost heartbeat.
2. **Best-effort API failure**: managed drain paths tolerate deregistration failure (warn-only) and terminate the container regardless — a transient rate-limit leak leaves a ghost.
3. **Supervisor down during the death**: `compose down` / host reboot with runners alive leaves registrations that nothing will ever clean up.

The ghosts do not self-heal: `--replace` only displaces a *same-name* registration, but every spawn generates a fresh unique container name, so ghosts are never displaced. GitHub ages out offline ephemeral runners only after a long window (~2 weeks), during which the Runners page is polluted.

## 2. Goals

- Detect and deregister orphaned `runnero-*` registrations automatically, within seconds-to-minutes of the leak.
- Reuse the M23 `RunnerLister` listing so the sweep adds **zero additional API calls** in steady state (one list per pool per audit cycle already happens for busy-state sync).
- Fail-open: a sweep problem must never disturb pool scaling or busy-state convergence.
- Coexist safely with manually registered runners and other automation on the same repository.

## 3. Non-Goals

- Removing GitHub's own slow auto-cleanup reliance for *busy* or *foreign* runners.
- Pool-level configuration UI for the sweep (a controller-level default is sufficient; a per-pool toggle can be added later as a DB column if demanded).
- Cross-provider parity: Gitea/Forgejo do not implement `RunnerLister` yet (docs/19 §8), so the sweep is GitHub-only at launch — additive later, exactly as with busy-state sync.

## 4. Design

### 4.1 Sweep rule

Every audit cycle, per pool, for each runner in the `RunnerLister` listing:

> Deregister if **all** hold:
> 1. `Online == false` (GitHub reports the registration offline),
> 2. `Busy == false` (never touch a runner that may be mid-job),
> 3. name matches the runnero naming scheme prefix `runnero-<pool-slug>-` (`GenerateContainerName`, `naming.go:36`) — foreign runners (manual registrations, other automation, other runnero deployments) are never touched,
> 4. the name is **not tracked locally** (drain logic and the offline guard own tracked runners; docs/19),
> 5. the runner has been observed offline-and-untracked for **N consecutive audit cycles** (default 3 ≈ 30 s at the 10 s default interval).

Condition 5 is the substitute for a grace timestamp: GitHub's runners listing carries no registration timestamp, so freshness is tracked in-memory — a per-pool map of `name → consecutive-offline-cycle count`. A runner that reappears online, becomes tracked, or turns busy resets its counter. Counters are dropped when the name disappears from the listing (GitHub removed it or it self-deregistered).

### 4.2 Placement and data flow

The sweep integrates into `reconcilePoolWithProvider`, immediately after the M23 busy-state sync, **consuming the same listing**:

```text
reconcilePoolWithProvider:
  gitProv := ResolveProvider(...)
  listing, err := listRemoteRunners(gitProv, p)     // NEW helper, extracted from syncRunnerBusyStates
  syncRunnerBusyStates(tracked, listing)            // existing M23 behaviour (no behavior change)
  ghostSweep(p, gitProv, tracked, listing)          // NEW
  ... classification/scaling unchanged ...
```

- The tracked snapshot is taken after the busy sync, as today; the sweep reads the same snapshot — a runner the supervisor still tracks is definitionally not a ghost, regardless of its remote status.
- Multi-target pools: the listing helper resolves and lists per target as in docs/19 §2.3; sweep decisions are per target. List failures are warn-only for that target (fail-open), matching M23 semantics.
- Paused pools do not run `reconcilePool`, so paused pools skip sweeps — same as scaling. Documented behaviour, not a special case.

### 4.3 Deregistration

- Delegates to the existing optional `RunnerDeregistrar` interface (`provider.go:79`); providers without it are never asked (sweep is a no-op), mirroring the optional-interface pattern of docs/19 §2.1.
- Errors are warn-only and retried naturally: the counter keeps the name in consecutive-offline state, so the next cycle re-attempts. A per-cycle cap (default 50 deregistrations) bounds the burst after mass-leak events (e.g. host reboot with a large pool), and the remainder are swept on subsequent cycles.
- Deregistration happens **before** any container action would — but ghosts have no container by definition; the sweep is purely an API-side reconciliation.

### 4.4 Relationship to existing cleanup paths

| Path | Deregisters via API today | After this design |
|---|---|---|
| Managed drains / shutdown (`controller.go` 619/725/965/1003/1015/1433) | ✅ | unchanged |
| Die-event reaping (`reapContainer`) | ❌ | unchanged; ghost caught by sweep ≤ N cycles later |
| Auditor `Disappeared` untracking (`auditor.go:108`) | ❌ | unchanged; ghost caught by sweep |
| Failed best-effort dereg during managed drain | retry only within that path | ghost caught by sweep on retry budget |
| Supervisor-down deaths | nothing | ghost caught after supervisor restart (counters reset; N cycles) |

Wiring the API into the death paths directly would close the window faster but duplicates provider-resolution plumbing in the Reconciler (which has no provider access); the sweep achieves the same end state with one code path and no new coupling. This is a deliberate trade: convergence in ~30 s instead of ~0 s.

## 5. Security Review

- **No new permission surface**: deregistration uses the same Administration(write) credential already required by the registration-token flow (docs/19 §3). The listing is read-only over the same endpoint as M23.
- **Trust boundary unchanged**: the sweep acts only on registrations in pools the supervisor already manages, over outbound API calls with credentials it already holds. No new inbound surface; no secrets in logs (ghost names only).
- **Blast radius of a bug**: worst case, the supervisor deregisters idle registrations it legitimately owns — the pool replenishes them (warm-standby cost), no job is lost (busy runners are exempt by rule 2), and no container is ever touched by the sweep. A malicious forge response can at most cause deregistration of `runnero-*`-prefixed idle registrations (availability), never execution or container access.
- **Coexistence**: rule 3 (name-prefix) makes interference with manually registered runners or a second runnero deployment impossible at the API level. Two runnero supervisors on one repo targeting each other's runners cannot happen: prefixes include the pool slug, and cross-deployment pools sharing a slug is an operator error within one trust domain (documented limitation).

## 6. Alternatives Considered

1. **Rely on GitHub auto-cleanup** — ~2-week latency; unacceptable dashboard hygiene.
2. **Deregister in the death paths directly** (`reapContainer` / auditor `Disappeared`) — closes the window to ~0 s but requires provider resolution + credentials inside the Reconciler/die-event plumbing (new coupling the Reconciler deliberately lacks today); also cannot cover supervisor-down deaths, which the sweep does.
3. **Runner-status webhooks** — GitHub offers no webhook for runner registration/heartbeat loss events.
4. **DB-backed ghost ledger** — persists across restarts, but in-memory counters cover the dominant paths after one sweep cycle post-restart; persistence adds schema and migration weight for negligible benefit. Revisit if multi-supervisor deployments appear.

## 7. Resolved Decisions (Design Review)

- **Design approved as proposed** (PR #178 merged without change requests):
  listing-driven sweep with the five-rule gate, consecutive-cycle grace
  instead of timestamps, fail-open retry semantics.
- **Implementation order:** RUN-114 (sweep engine + tests) → RUN-115
  (docs, this change).

## 8. Implementation Notes (as-built)

- **Structure:** `syncRunnerBusyStates` was split into `fetchRemoteRunners`
  (shared listing helper returning `(listing, target)`) and
  `applyRemoteBusyState` (busy convergence, semantics unchanged);
  `ghostSweep` consumes the same listing immediately after. Both run in
  `reconcilePoolWithProvider` before classification feeds scaling.
- **API-call note:** pools with zero tracked runners previously skipped the
  listing entirely; they now list once per cycle so scaled-to-zero pools
  still sweep. Steady-state cost remains one list per pool per cycle.
- **Counters** live in-memory on the controller (`ghostCounters`), guarded
  by a dedicated mutex held for the whole sweep body — `reconcilePool` runs
  concurrently (control loop + die-event handler), and the lock pattern
  matches the existing drain paths, which already perform network calls
  under lock.
- **Exactly-once:** a successful deregistration drops the counter; the
  listing then drops the name, keeping a re-registration start fresh. The
  mock listing is static, so tests model forge removal explicitly.
- **Tests (6, controller-level):** sweep lands on the Nth consecutive
  offline cycle and fires exactly once; tracked (even offline-at-forge),
  busy, and foreign-name entries are never swept; a single online cycle
  resets the counter (flap protection); deregistration failure keeps the
  counter and retries next cycle; busy sync + sweep share exactly one list
  call per cycle.
- **Config surface:** `ControllerOptions.GhostSweepOfflineCycles` (default
  3) and `GhostSweepMaxDeregistrations` (default 50); non-positive values
  fall back to defaults. No DB schema change.
