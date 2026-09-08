# 24. Demand Polling Fallback: Scale Webhook Pools Without Inbound Webhooks

| | |
| :--- | :--- |
| Status | Design Phase |
| Touches | `internal/orchestrator` (scaling gate, throttling), `internal/provider` (`PollQueuedJobs` interface + GitHub impl), `internal/db` (migration 006, sqlc), `internal/server` (pool RPCs), `web` (wizard & edit), docs/03 §3b, docs/14, docs/16, docs/22 |

## 1. Problem

Webhook-driven pools (GitHub, Gitea) scale only when the forge can deliver
`workflow_job` events to `POST /hooks/{provider}`. On workstations, NAT'd or
firewalled hosts, and air-gapped networks, inbound webhooks never arrive — so a
pool sits at `min_idle_runners` forever while jobs queue on the forge. Observed
on the workstation pool `kraken-runners` (GitHub, `noosxe/my-dashboard`): queued
jobs are never picked up beyond the single warm standby; no capacity is added,
no diagnostics explain why.

A polling scaling engine already exists (docs/03 §3b, RUN-70) but is keyed to
the **provider type**, not deployment reality: only Forgejo pools poll
(`gitProv.ScalingMode() == provider.ScalingPolling` gates the branch in
`PoolController`), and `PollQueuedJobs` is a hard no-op for GitHub and Gitea.
A GitHub pool has no way to opt into demand detection without webhooks.

## 2. Goals

- Pools backed by webhooks can scale without inbound connectivity: the
  supervisor periodically scans each pool's repo targets for queued jobs and
  provisions runners up to capacity — the same outcome as the webhook route,
  slower.
- Fully compatible with the webhook fast path: when webhooks *do* work, polling
  is a redundant safety net, not a conflicting controller; both converge on the
  same replenisher, quota, and `max_concurrency` logic.
- Per-pool opt-in and per-pool cadence; pools that don't enable it keep today's
  behavior and API quota profile exactly.
- Label-correct demand counting on GitHub: only jobs whose `runs-on` labels a
  pool's runners can actually satisfy are counted.
- Failure modes are diagnosable in pool diagnostics (auth, rate limits,
  unsupported scopes) instead of silent no-ops.

## 3. Non-goals

- Replacing or degrading the webhook fast path; webhooks remain the sub-second
  route (docs/03 §3b).
- Polling for **org- or instance-scoped** GitHub targets (no forge API exists —
  see §4). Org targets keep webhook-only demand.
- Gitea support in v1 (no repo-scoped queued-jobs API exists — see §4).
- Job *result* tracking via polling; job start/end detection stays with busy
  sync (docs/19) and webhooks.
- Any new inbound network surface; polling is outbound read-only.

## 4. Verified provider API matrix

Verified against pinned upstream descriptions (GitHub
`rest-api-description@main`, `gitea.com/swagger.v1.json`,
`codeberg.org/swagger.v1.json`):

| Provider | Repo-level queued-jobs query | Notes |
| :--- | :--- | :--- |
| GitHub | Two-step: `GET /repos/{o}/{r}/actions/runs?status=queued` → `GET /repos/{o}/{r}/actions/runs/{run_id}/jobs` | Job objects carry `status` (`queued`/`in_progress`/…), `labels` (from `runs-on`), and `runner_name` (null until assigned). **No** repo-level jobs listing and **no** org-level runs listing exist in the spec. Classic PATs need `repo`; fine-grained need `Actions: read`. |
| Gitea | None repo-scoped; only `/user/actions/jobs` and `/user/actions/runs` (authenticated user's own jobs) | Cannot answer "queued jobs for target repo" — unsupported in v1. |
| Forgejo | `GET /repos/{o}/{r}/actions/tasks` (+ org/admin variants), `status=waiting` | Already implemented (`internal/provider/forgejo`); counts all waiting tasks, no label filter. |

## 5. Design

### 5.1 Per-pool `poll_fallback` toggle

New pool column `poll_fallback BOOLEAN NOT NULL DEFAULT 0` plus
`poll_interval_seconds INTEGER NOT NULL DEFAULT 30` (CHECK: 15–3600). The
controller's polling gate changes from a provider-type test to:

```go
pollsDemand := p.PollFallback || gitProv.ScalingMode() == provider.ScalingPolling
```

- **GitHub pools:** user-togglable. Off by default — preserves the current
  quota profile for deployments with working webhooks.
- **Forgejo pools:** always-on regardless of the flag (the provider has no
  webhook demand signal); the UI shows polling as built-in rather than a
  toggle.
- **Gitea pools:** toggle disabled with an explanatory tooltip (§5.7);
  validation rejects enabling it via RPC.

Inbound webhooks remain accepted for *all* pools in all modes. A `polling`
pool with working webhooks simply gets both signals converging (§5.5); there
is deliberately no `webhook`-only-vs-`polling`-only distinction to configure.

### 5.2 GitHub `PollQueuedJobs`

Implement the two-step query against the existing pool credentials:

1. `GET /repos/{o}/{r}/actions/runs?status=queued&per_page=50` — pending runs.
   Send `If-None-Match` from the previous poll's `ETag`; a `304` skips step 2
   and counts as a zero-cost no-change poll (304s do not count against the
   primary rate limit).
2. For each run, `GET /repos/{o}/{r}/actions/runs/{run_id}/jobs?per_page=100`
   and count jobs with `status == "queued"` **whose labels the pool can
   satisfy**, reusing `LabelsMatch` (`internal/orchestrator/webhook_scaling.go`):
   a job whose `runs-on` demands a label the pool doesn't configure would
   never be picked up by a spawned runner, so counting it would overprovision.

Targets that are org/instance-scoped are skipped with a pool diagnostic
(`poll skipped: no org-level queued-jobs API`), never an error-level failure.
The org's repos aren't enumerable within scope, so the webhook route remains
the only demand signal for those targets.

### 5.3 Interface change

`PollQueuedJobs` grows from `(ctx, targetURL string) (int, error)` to accept
the pool's label contract and scope:

```go
type PollTarget struct {
    URL    string
    Scope  RegistrationScope // repo | org | global
    Labels string            // pool labels JSON, as stored
}
PollQueuedJobs(ctx context.Context, target PollTarget) (int, error)
```

Forgejo's implementation ignores `Labels` for now (documented limitation:
counts all waiting tasks; label-aware filtering is tracked as RUN-144).
Gitea returns a typed
`ErrPollingUnsupported`; the controller surfaces it as a pool diagnostic if
`poll_fallback` somehow ends up enabled.
### 5.4 Cadence and throttling

The audit cycle (~10 s) remains the driver — no new goroutine/timer
machinery. Each pool tracks `lastPollAt`; the audit cycle polls a pool only
when `now - lastPollAt >= poll_interval_seconds ± 20% jitter`. Jitter
desynchronizes multi-target pools hammering the same API host in lockstep.
Worst-case pickup latency is one interval (default 30 s) plus the ~10–15 s

The interval is a DB-level knob in v1 (default 30 s, CHECK 15–3600); it is
not exposed in the wizard or edit UI — surface it only if an operator asks.
runner boot — acceptable for a fallback path, and the property that matters:
it is bounded, unlike "never".

### 5.5 Convergence with the webhook fast path

Demand counting is inherently self-deduplicating: only `status == "queued"`
jobs count, and once a job is picked up (by a webhook-spawned runner or a warm
standby) it leaves the queued set. The known race — job *assigned* but still
reported `queued` for the ~5 s busy-sync lag (docs/19; the RUN-143 incident
measured ~4.8 s) — can produce one surplus standby. That standby is absorbed
by existing mechanics: it idles warm, and the over-target recycle path
(active runners above `min_idle` + queued demand) drains it later. No new
anti-flap logic is introduced.

### 5.6 Capacity semantics — unchanged, just reachable

The existing polling branch already implements the correct arithmetic:
`deficit = max(0, queued − idle)`, `effectiveTarget = max(min_idle,
active + deficit)`, capped by `max_concurrency`, spilling into the global
quota queue when saturated. This doc only changes **when that branch runs**
(provider-type gate → per-pool gate) and makes its GitHub input real. The
`onDemand` expression (`controller.go`, drain/grace semantics per RUN-71)
gains the same condition: `p.MinIdleRunners == 0 || pollsDemand`, so
deficit-spawned runners on fallback pools are preserved through their startup
grace instead of being drained as surplus.

### 5.7 Validation rules (create + edit)

The UI exposes only the `poll_fallback` toggle; the interval stays a
DB-level knob (§5.4).

| Provider | `poll_fallback` |
| :--- | :--- |
| GitHub, repo-scope targets | editable, default off |
| GitHub, any org/global target | editable but diagnostics report skipped targets |
| Gitea | rejected (`polling unsupported for gitea`) |
| Forgejo | not applicable — always on |
### 5.8 Data model & migration

`006_demand_polling_fallback.sql`:

```sql
ALTER TABLE runner_pools ADD COLUMN poll_fallback BOOLEAN NOT NULL DEFAULT 0;
ALTER TABLE runner_pools ADD COLUMN poll_interval_seconds INTEGER NOT NULL DEFAULT 30
    CHECK (poll_interval_seconds BETWEEN 15 AND 3600);
```

Defaults preserve today's behavior for every existing pool. sqlc queries for
pool create/update/get/list gain both columns.

### 5.9 RPC & UI surfaces

- Pool create/update RPC messages: optional `poll_fallback`,
  `poll_interval_seconds` fields (backward-compatible defaults); server-side
  validation per §5.7; audited as part of the existing `pool.create` /
  `pool.update` audit events.
- Wizard (docs/14) and edit workflow (docs/22): one checkbox — "Scale without
  webhooks (poll for queued jobs)" — rendered per the §5.7 matrix. Forgejo
  shows polling as an inherent capability, not a toggle.
- Pool diagnostics (docs/16): expose `last_poll_at`, `last_poll_queued_count`,
  `last_poll_error` so "why isn't it scaling" is answerable from the UI.
### 5.10 Failure modes & observability

| Failure | Behavior |
| :--- | :--- |
| 401/403 from forge | Pool diagnostic (`auth` error code), polling continues at cadence |
| 403 secondary rate limit / 429 | Back off that pool by one interval per consecutive failure (cap: 5× interval), diagnostic set |
| Network unreachable | Diagnostic + Warn log, no spawn decisions made from stale data |
| Org-scope target | Skipped with diagnostic (not an error) |

Every poll that changes the spawn decision logs the existing
"polling detected queued jobs exceeding idle runners" line with counts.

### 5.11 Security implications

- **No new inbound surface.** Polling is outbound HTTPS using the pool's
  existing stored credentials; zero-leak policy unchanged.
- **Least privilege:** runs/jobs listing is read-only. Classic PAT `repo`
  scope or fine-grained `Actions: read` — narrower than the registration
  flows the pool already performs. Documented in the wizard help text.
- **Quota hygiene:** per-pool interval floor (15 s), ETag conditional
  requests, jitter, and rate-limit backoff bound the polling footprint;
  defaults keep a single-repo pool at ~2 requests/30 s (1 with `ETag` hits).

### 5.12 Testing

- **Unit (controller):** gate matrix (provider × `poll_fallback` × scope),
  throttle + jitter, deficit math on mixed targets, onDemand interaction,
  webhook+poll convergence (queued → in_progress removes demand; surplus
  standby drains via existing recycle).
- **Unit (GitHub client):** httptest-backed two-step query — queued filtering,
  `LabelsMatch` integration (unsatisfiable labels not counted), pagination,
  `ETag`/304 short-circuit, error mapping, org-target skip.
- **Unit (validation):** §5.7 matrix on create/edit RPCs.
- **E2E:** extend the mock forge (`tests/e2e/mock/provider`) with queued-jobs
  endpoints only if cheap; otherwise covered at the unit level with the fake
  client, and a manual checklist item on the implementation PR (disable
  webhooks on a real pool, push a workflow, watch scale-out within
  `poll_interval + boot`).

## 6. Alternatives considered

- **Always-on auto-polling for every pool** — simplest, but silently spends
  every deployment's API quota and makes the webhook investment pointless;
  also changes behavior of existing pools without consent.
- **Webhook-health auto-detection** (poll only when deliveries look stale) —
  clever, but the "stale" heuristic is guesswork; explicit operator intent is
  debuggable and matches "workstation pool" reality.
- **Scale-on-webhook-timeout only** — doesn't help when webhooks never arrive,
  which is the entire problem.

## 7. Resolved decisions

Recorded during design review (PR #200):

1. **Interval default 30 s** — confirmed.
2. **Interval UI** — not exposed in v1; DB-level knob only (§5.4).
3. **Gitea** — no release tracking; if a repo-scoped Actions listing API
   ever lands, file an issue then.
4. **Forgejo label-aware filtering** — filed as RUN-144.
