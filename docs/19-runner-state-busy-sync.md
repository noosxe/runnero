# Runner State Busy Sync (docs/19)

| | |
| :--- | :--- |
| **Status** | Shipped — implemented on `feature/runner-state-busy-sync` (design approved via PR #176) |
| **Milestone** | Runner State Busy Sync (RUN-111 → RUN-113) |
| **Related** | `docs/03` §3 (Audit Engine), `docs/02` §3 (Abstractions) |
| **Bug** | Runners executing jobs are shown as `idle` in the supervisor UI/API |

## 1. Problem Statement

The supervisor's `RunnerStatus.IsBusy` flag — the only source of the `busy`
vs `idle` instance status exposed via `ListPoolRunners` and the web UI — is
set by exactly one code path today: GitHub `workflow_job` **webhooks**
(`internal/orchestrator/webhook_scaling.go`, `in_progress` → busy,
`completed` → idle).

Everything else in the state pipeline is blind to job activity:

1. **The audit engine only inspects Docker container state** (`running` /
   `exited`). A container remains `running` whether the runner process inside
   is idle or mid-job, so the periodic audit can never *set* busy. The
   auditor's preserve rule (keep `existing.IsBusy` when the fresh live status
   is not busy) only retains a flag someone else set.
2. **The provider layer never queries runner state.** The `GitProvider`
   interface has no runner-listing capability, so GitHub's
   `GET .../actions/runners` API — which returns `busy: true/false` and
   `status: online|offline` per registered runner — is never called.

Consequences:

- **Without webhook delivery** (webhook not configured, secret mismatch,
  unreachable supervisor URL, proxy/DNS issues), every runner shows `idle`
  forever, regardless of activity.
- **With webhooks**, a single missed `completed` event leaves a runner stuck
  `busy` (sticky-wrong), and a missed `in_progress` shows idle — there is no
  convergence mechanism.
- **Scaling correctness is degraded by the same blind spot:** the per-pool
  reconcile loop classifies tracked `running` containers into busy/idle and
  may drain "idle" runners (scale-down, `max_concurrency` enforcement). A
  runner whose busy flag was missed is treated as idle and can be
  **terminated mid-job**.

## 2. Proposed Design

Make the provider API the **authoritative, periodically-polled source** of
busy state; webhooks remain the sub-second **fast path**. Both write through
the same `Reconciler.MarkRunnerBusy`, and every audit cycle converges state,
healing both stuck-idle and stuck-busy conditions.

### 2.1 Provider surface (optional interface)

```go
// provider package

// RemoteRunnerStatus is the provider-reported state of one registered runner.
type RemoteRunnerStatus struct {
    Name   string // runner name as registered at the forge
    Busy   bool   // currently executing a job
    Online bool   // reachable / recently contacted by the forge
}

// RunnerLister is optionally implemented by GitProviders whose API exposes
// registered-runner state (docs/19 §2). Callers must type-assert.
type RunnerLister interface {
    ListRunners(ctx context.Context, scope RegistrationScope, targetURL string) ([]RemoteRunnerStatus, error)
}
```

Optional-interface pattern, mirroring `RunnerDeregistrar` (docs/03 §7):
providers that cannot answer are simply never asked; nothing breaks.

### 2.2 Provider implementations

- **GitHub** (initial scope): `ListRunners` on the existing `Client` —
  endpoint by scope: `GET /repos/{o}/{r}/actions/runners`,
  `GET /orgs/{org}/actions/runners`,
  `GET /enterprises/{e}/actions/runners`; paginated at `per_page=100`
  (bounded page cap as loop-guard); maps `busy` and `status == "online"`.
  Authentication reuses the existing token resolution
  (`getAuthBearerToken`: GitHub App installation token or PAT) — the
  endpoint requires the **same Administration (write) permission the
  registration-token flow already requires**, so **no new permission
  surface** is introduced.
- **Gitea / Forgejo** (same PR if straightforward, else fast-follow): their
  `/actions/runners` list endpoints (repo / org / admin scopes) expose a
  runner `status` string; mapping `busy` → `Busy`, `offline` →
  `Online=false`. Exact field names verified at implementation time; if an
  API turns out not to expose state, the provider simply does not implement
  `RunnerLister`.

### 2.3 Orchestration integration (audit loop)

In `PoolController.reconcilePool`, **before** the busy/idle classification
feeds scaling decisions, a new step runs per pool:

```go
c.syncRunnerBusyStates(ctx, gitProv, p, targets)
```

Semantics:

1. For each pool target, call `ListRunners` once (first target that succeeds
   satisfies the pool — all runners in a pool register under the same
   scope/target set; remaining targets are only tried on failure).
2. Build a `name → RemoteRunnerStatus` map and apply to every tracked runner
   in the pool: `MarkRunnerBusy(name, busy)`.
3. **Offline guard:** if the provider reports `Online=false`, skip applying
   (leave current state). This protects against clobbering a webhook-set
   `busy=true` during registration/contact races, and against an
   `offline + busy=false` snapshot tearing down a healthy state.
4. **Names absent from the map** are left untouched — deregistration/removal
   is owned by the existing lifecycle flow, not by list absence.
5. **Errors are warn-only** and never block the reconcile; the loop
   fails open to the last known (webhook or default idle) state. A pool
   error counter entry is *not* raised for lister failures (they are
   transient by nature).
6. **Lifetime anchor stamping (docs/23 §4.2)**: the idle→busy transition
   applied by this sync (or the webhook fast path) also stamps the runner's
   busy anchor (`BusySince`), set-once. A missed webhook therefore delays the
   anchor — and the kill deadline — by at most one audit cycle.

### 2.4 Consistency semantics

| Scenario | Behaviour |
| :--- | :--- |
| Webhook `in_progress` then poll says busy | Agree — no-op |
| Webhook missed; poll sees busy | Idle → busy within ≤1 audit cycle (10s default) |
| Webhook `completed` missed; poll sees idle | Stuck busy → healed within ≤1 cycle |
| Poll races a just-delivered webhook (stale snapshot) | Transient flap, self-corrects next cycle (≤10s) |
| Provider unreachable | Last known state stands; webhook still updates it |
| Runner reported offline | State preserved (offline guard) |

The worst-case flap window equals the audit interval; no ordering or
generation counters are introduced — not worth the complexity at a 10s
convergence bound.

### 2.5 Non-goals

- **No proto/UI changes**: `RunnerInstance.status` already renders
  `"busy"`; `PoolState.idle_runners` counts immediately become more accurate
  with no schema change.
- **No container-internal probing** (e.g. `docker exec` into the runner,
  log scraping): fragile, crosses the container trust boundary for
  observation, and provider APIs answer the question directly.
- **No job-level tracking** (which job, run ID, duration): the webhook
  recorder already captures job events when delivered; this design is
  strictly about runner busy state.

## 3. Security Review

- **Read-only outbound API calls** to the forge the pool already talks to,
  with credentials the supervisor already holds and uses for the same
  targets (registration tokens, `PollQueuedJobs`). No new inbound surface,
  no new credential types, no permission escalation (GitHub:
  Administration-write, already required today).
- **Secrets handling reuses existing, leakage-tested paths** (`leakage_test`
  suite covers the request/token plumbing); runner names and states are
  non-sensitive metadata already present in logs today.
- **Trust boundary unchanged:** the forge remains the authority on runner
  state; the supervisor only *observes* it. A compromised/malicious forge
  response can at worst flip busy flags (availability), not gain execution
  or reach containers.
- **Rate-limit safety:** one list call per target per audit cycle (≤6/min
  per target at the 10s default), paginated with a bounded page cap;
  secondary-rate guidance respected by the existing single-request-per-cycle
  pattern.

## 4. Alternatives Considered

| Alternative | Verdict |
| :--- | :--- |
| Keep webhooks as sole source; require webhook setup | Rejected — lossy, no convergence, silent failure mode is exactly the reported bug |
| Container-internal probing (`docker exec` / log scan) | Rejected — crosses container boundary, brittle across runner versions, no forge authority |
| Infer busy from job-recorder timeout bookkeeping | Rejected — only covers jobs the webhook path already saw; adds inference on inference |
| Query runs/jobs API per repository (`actions/runs`) | Rejected — N calls per repo, heavier, mapping jobs→runners is indirect vs the runners list that names them |

## 5. Test Plan

- **GitHub client** (httptest): endpoint shape per scope (repo/org/
  enterprise), pagination, `busy`/`status` mapping, auth header path.
- **Controller** (existing suite patterns, fake `RunnerLister`):
  - busy sync flips tracked state before scaling classification;
  - a busy-marked runner is **not** drained by scale-down /
    `max_concurrency` enforcement (regression for the mid-job termination);
  - offline guard preserves state; absent names untouched;
  - lister error → warn-only, reconcile proceeds.
- **Full mandated suite** (`make test && make test-scripts &&
  make test-web && make lint && make lint-web && make vet && make build &&
  make clean && shellcheck src/*.sh && shfmt -d src/*.sh && hadolint
  Dockerfile`).

## 6. Rollout & Compatibility

- Zero configuration; zero migration; optional interface so providers
  without support behave exactly as today.
- No proto/DB changes; UI picks up corrected states automatically via
  existing watch/poll channels.

## 7. Resolved Decisions (Design Review)

- **Design approved as proposed** (PR #176 merged without change requests):
  authoritative provider-API poll converged every audit cycle, webhooks as
  fast path, offline/absent-name guards, fail-open error semantics.
- **Implementation order:** RUN-111 (provider surface + GitHub client) →
  RUN-112 (audit-loop integration) → RUN-113 (docs, this change).

## 8. Implementation Notes (as-built)

- **GitHub is the only `RunnerLister` implementation for now.** Gitea and
  Forgejo do not implement the optional interface yet — their runner-list
  APIs' state semantics vary across versions; per the design they are simply
  never asked, so behaviour for those providers is unchanged. Wiring them up
  later is additive and does not touch the orchestrator.
- **Sync placement:** the call sits at the top of
  `reconcilePoolWithProvider`, immediately after the engine/reconciler nil
  guard, and the tracked snapshot is taken *after* the sync so scaling
  classification always reads post-convergence state.
- **No per-cycle dedup needed:** `MarkRunnerBusy` is only called when the
  tracked flag actually differs from the listing, so steady-state cycles
  perform no writes.
- **Rate-limit footprint:** one list call per pool per audit cycle (first
  successful target short-circuits multi-target pools); pagination is
  bounded at 50 pages × 100 runners.
- **Tests:** 5 client tests (scope endpoints, auth header, pagination across
  two pages, busy/online mapping, repo-scope validation, API errors) and 5
  controller tests (pre-classification flip; the scale-to-zero mid-job-drain
  regression in both directions; offline/absent guards; lister-error
  fail-open; provider-without-lister untouched). `mockGitProvider` gained
  `ListRunners` so every existing controller test exercises the sync path
  with an empty listing (a no-op).
