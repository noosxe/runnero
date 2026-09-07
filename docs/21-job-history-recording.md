# Job History Recording (docs/21)

> **Status: Design Phase** — awaiting review. Implementation must not start before
> this document is approved and merged.

## 1. Problem Statement

The dashboard overview KPIs (jobs executed, success rate, average runtime, average
queue wait, queue-latency trend) and the History page read the `job_history` table.
Investigation (2026-09) found that the table has **exactly one writer**:
`RecordJobTimeout` (internal/db), invoked only from the hung-runner kill switch
(`checkHungRunners`, RUN-71). Normal job executions are **never recorded**, so every
DB-derived indicator is permanently zero while only the live-state indicators
(active / warm idle runners, fed by `SystemRunnerStats()`) work.

Meanwhile the supervisor receives complete job-lifecycle data and discards it:

- `workflow_job` webhooks (`queued` / `in_progress` / `completed`, M11 receiver at
  `POST /hooks/{provider}`) carry job id, run id, workflow name, branch, SHA,
  conclusion, and runner name — the handler consumes them for scaling decisions
  and throws the rest away.
- The busy-state sync (docs/19) observes every idle→busy and busy→idle transition
  of tracked runners on each audit cycle — an implicit job-start / job-end stream
  that works **without webhooks**.

## 2. Deployment Reality: Two Modes

This design is explicitly shaped around the fact that many deployments — including
typical local/test machines behind NAT — **never receive webhooks**:

| | Webhookless (default) | Webhook-enabled |
|---|---|---|
| Reachability | Supervisor not reachable from forge | Public URL (reverse proxy) or tunnel (`gh webhook forward`) |
| Job dispatch | Forge's native scheduler assigns queued jobs to registered idle runners | same |
| Scaling signals | Audit-cycle polling only (queued-job poll where the provider implements it; busy-state sync per docs/19) | Sub-second `workflow_job` events |
| Busy-state latency | ≤ 1 reconcile cycle (~10 s) | Sub-second |
| Availability | **Primary assumption for this design** | Optional enhancement |

Consequences:

1. The **primary recorder must be provider-agnostic and webhook-independent** —
   derived from busy-state transitions the supervisor already observes.
2. Webhooks are an **enrichment and fast path**: authoritative timestamps, external
   job ids, and conclusions. Without them the dashboard still works, with slightly
   reduced fidelity (no queue wait, conclusion resolution deferred to §5.3).

## 3. Goals

- G1: Every observed job execution produces exactly one `job_history` row in every
  deployment mode (webhookless included).
- G2: Dashboard KPIs, queue-latency trend, success/failure widget, and History page
  display truthful data; unavailable metrics degrade to an explicit "—" instead of
  fabricated zeros or misleading 100%.
- G3: Recording never blocks or breaks provisioning, reconciliation, or shutdown
  paths; all recording work is fail-open.
- G4: No new secrets, no new public endpoints, no webhook requirement.
- G5: Existing rows (the rare `timeout` rows) survive migration.

## 4. Non-Goals

- Replacing the forge's own job history/analytics; we record what the supervisor
  observes.
- Workflow log capture or artifact storage beyond the existing `log_retention_path`
  mechanism.
- Retroactive backfill of jobs that ran before this feature ships.

## 5. Design

### 5.1 Status vocabulary

`job_history.status` is extended:

```
queued | running | success | failure | cancelled | timeout | completed | interrupted
```

- `running` — row is open (job observed started, not yet ended). At most one open
  row per runner at any time (partial unique index).
- `completed` — ended; outcome unknown (enrichment unavailable or disabled).
- `interrupted` — supervisor restarted or runner died with a stale open row
  (crash recovery, §5.4); outcome unknown.
- `success` / `failure` / `cancelled` / `timeout` — known outcomes; the set of rows
  eligible for the success-rate denominator.

### 5.2 Primary recorder: busy-state transitions (webhookless-safe)

Lives next to the existing busy-state reconciliation (docs/19 machinery, single
per-cycle remote listing — no extra API calls):

- **idle→busy** on tracked runner `R` (pool `P`): open one row —
  `pool_id=P`, `runner_name=R`, `status='running'`, `started_at=now`.
  `queued_at` stays NULL (the supervisor cannot observe queue entry in this mode;
  queue wait is reported as unavailable rather than fabricated).
- **busy→idle** on `R`, or **container death while `R` has an open row**
  (ephemeral runners exit after their job; die-event and reap paths both already
  run): close the open row — `completed_at=now`, runtime = completed − started,
  status resolved per §5.3 / §5.4.
- The hung-runner path (`RecordJobTimeout`) keeps writing `timeout` rows and now
  also closes any open row for that runner (no duplicates).
- Merge rule: when a `workflow_job` event names the same runner (§5.5), it attaches
  to the open transition row instead of creating a second one.

### 5.3 Conclusion enrichment (optional provider capability)

Closing a job without a webhook leaves the outcome unknown. To recover `success` /
`failure` without user-visible loss, add an optional provider capability:

```
RunnerLatestJobs(ctx, targetURL, runnerName) ([]RunnerJob{ID, Conclusion, CompletedAt}, error)
```

- GitHub implementation: `GET /repos/{owner}/{repo}/actions/runners/{runner_id}/jobs`
  (recent jobs incl. `conclusion`). Requires the runner id, which the existing
  `RunnerLister` listing already carries — persisted onto tracked runner state.
- Invoked **once per job completion** (not per cycle) with a bounded timeout; the
  result sets status + external `job_id` on the closing row.
- Failure or unimplemented provider → row closes as `completed` (fail-open, G3).
- Globally toggleable via config (default: on where implemented).

### 5.4 Crash recovery

Open rows must not survive forever:

- At boot, after the initial convergence pass: any row with `started_at` set and
  `completed_at` NULL whose runner is not currently tracked-busy is closed as
  `interrupted` with `completed_at=now`.
- Belt-and-braces: a row open longer than `2 × max_runner_lifetime_seconds`
  (pool-configured, when set) is force-closed as `interrupted` by the audit loop.

### 5.5 Webhook enrichment (fast path, optional)

`WorkflowJobPayload` gains the forge-provided timestamp fields
(`created_at`, `started_at`, `completed_at`). Handler changes:

- `queued`: upsert by external `job_id` — `queued_at`, `status='queued'`, workflow
  metadata (`workflow_name`, `head_branch`, `head_sha`, `run_id`), pool resolved
  via the existing `MatchPoolForEventWithTargets`.
- `in_progress`: set `started_at`, `runner_name`, `job_id`. If the runner already
  has an open transition row (webhook arrived after the poll noticed, or vice
  versa), the event attaches to that row instead of creating a new one.
- `completed`: set `completed_at` + map `conclusion` → status
  (`success`/`failure`/`cancelled`, others → `completed`), close the row, and close
  the runner's open slot.
- Deduplication invariant (all writers): **at most one open row per runner**
  (partial unique index `WHERE completed_at IS NULL`). Webhook rows created before
  a transition row for the same job are merged by job id; transition rows never
  carry a fabricated `queued_at`.

### 5.6 Stats queries and UI semantics

- `GetJobStatsSince` / `GetHourlyJobStatsSince` redefined:
  - **Jobs executed** = all closed rows in the window (any terminal status).
  - **Success rate** = `success / (success + failure + cancelled + timeout)` —
    known-outcome rows only.
  - **Avg runtime** = rows with both timestamps.
  - **Avg queue wait** = rows with `queued_at` (webhook mode only; "—" otherwise).
- Frontend: success-rate and average cards render `—` (not `100`/`0`) when their
  denominators are empty; sub-labels say "no concluded jobs in window" /
  "queue timing requires webhooks".

### 5.7 Schema migration

Single SQLite migration (recreate-table pattern):

- New columns: `job_id BIGINT` (nullable, indexed), `run_id BIGINT`, `workflow_name TEXT`,
  `head_branch TEXT`, `head_sha TEXT`, `source TEXT CHECK IN ('transition','webhook','timeout')`.
- Extended `status` CHECK per §5.1.
- Partial unique index: one open row per `(pool_id, runner_name)`.
- Existing data preserved; legacy rows without `source` backfilled as `'timeout'`
  (they only ever came from the hung-runner path).
- sqlc queries regenerated; `docs/07-database-schema.md` updated.

## 6. Security Review

- No new secrets or credentials; enrichment reuses the provider client already
  authenticated for listing (Administration(read) scope already held for
  registration-token fetching).
- No new inbound endpoints; webhook receiver unchanged (HMAC verification, M11).
- Stored data is job metadata (ids, names, branches, SHAs, timings, conclusion) —
  the same information visible to the user in the forge UI. No job logs, secrets,
  or environment data are persisted; `log_retention_path` semantics unchanged.
- Enrichment API cost is bounded (one call per job completion) and executed under
  the existing single-writer semantics with a timeout budget; a forge outage can
  only degrade rows to `completed`, never stall provisioning (fail-open).
- Trust boundary note: webhook payloads were already trusted post-HMAC for scaling;
  they now additionally write metadata rows. Only fields from the verified payload
  are stored; no free-form fields are executed or rendered as HTML by the SPA
  (React-escaped).

## 7. Alternatives Considered

- **Webhook-only recorder** — rejected as primary: dead on webhookless deployments
  (the most common local/test topology, and the one that surfaced this bug).
- **Poll the forge's jobs list per cycle** — rejected: continuous API cost per pool
  per cycle for data we already observe via busy transitions; also misses runner
  identity → job mapping for pooled ephemeral runners.
- **Container exit-code inference for success/failure** — rejected: the runner
  listener's exit code reflects deregistration, not job conclusion.

## 8. Resolved Decisions (Design Review)

*(to be filled during review)*

## 9. Implementation Plan

- **Phase 1 — transition recorder**: schema migration, transition open/close in the
  orchestrator, crash recovery, query + UI semantics (§5.6), stats tests.
- **Phase 2 — webhook enrichment**: payload timestamp fields, upsert/merge rules,
  dedup invariant tests.
- **Phase 3 — conclusion enrichment**: `RunnerLatestJobs` capability (GitHub first),
  config toggle, fail-open tests.
- Phases are independently shippable; Phase 1 alone makes every dashboard indicator
  live in webhookless mode (except queue wait, which honestly reports unavailable).
