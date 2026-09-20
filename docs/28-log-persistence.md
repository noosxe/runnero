# Durable Supervisor & Runner Logs (RUN-186)

Status: design — RUN-186. UI observability of these logs is deliberately out of
scope and tracked as RUN-213.

## 1. Summary

Persist supervisor and runner container logs across supervisor recreation and
runner removal, so an operator can answer **"what happened to runner X in its
last job, and why was it removed"** from files that survive on the already
persistent `/data` volume.

Three pieces, delivered together:

1. **Supervisor log persistence** — the structured log stream is mirrored to
   boot-separated files under `/data/logs/supervisor/` in addition to stdout.
2. **Runner log capture** — a runner container's stdout is captured to
   `/data/logs/runners/` before the container is removed, on every removal
   path, including removals performed by a freshly (re)created supervisor.
3. **Removal decision records** — every removal appends a structured,
   greppable record (runner id, provider busy flag, drain reason, veto
   outcome, capture outcome) to `/data/logs/removals.jsonl`.

## 2. Motivation

A dogfood runner (`kraken-runners-cf3fb8`) died mid-job and left a ghost
GitHub registration that swallowed a dispatched job. The post-mortem was
impossible: the supervisor container had been recreated, and `docker json-file`
logs die with the container. Runner containers are removed after
drain/idle-reap, so their stdout is unrecoverable by design. The RUN-182-class
veto decisions and deregistration reasons live only in logs that get deleted.

## 3. Constraints from operations

- **The supervisor may be restarted/recreated at any time** (image updates,
  `compose up`). Nothing may depend on process-lifetime state: sinks open in
  append mode, boot separation is by file, and retention/reconciliation sweeps
  run at every boot.
- **Runners, not the supervisor, are the things that die unexpectedly** — and
  when they do, forensics are expensive. Runner-side durability is the
  priority; solid supervisor logs are the baseline that explains what the
  supervisor itself did.
- Runner containers are created with an empty `HostConfig`: **no auto-remove
  and no restart policy**. A dead runner therefore lingers as a stopped
  container until the supervisor explicitly removes it. This property is what
  makes capture-at-removal sufficient — see §5 for why streaming was rejected.

## 4. Storage layout

Everything lives under the existing data dir (`data-dir`, default `/data`),
next to `backups/` and `tailscale/`:

```
/data/logs/
├── supervisor/
│   └── boot-<unix-ts>-<bootid8>.ndjson    # one file per supervisor boot
├── runners/
│   └── <provider>-<pool-id>-<runner-id>-<unix-ts>.log   # raw stdout capture
└── removals.jsonl                          # structured removal records
```

- **`bootid`** is a fresh random 128-bit id generated at every startup (like
  the existing boot-time key derivation). First line of every boot file
  carries `{boot_id, started_at, version}` for orientation.
- Runner capture files are the container's **raw stdout** (not re-encoded) so
  nothing is lost to format conversions; their metadata lives in
  `removals.jsonl`, not in the file.
- File mode `0600`, directory mode `0700` — same trust domain as the SQLite
  database and backups already on `/data`.

## 5. Design decisions

### 5.1 Supervisor log persistence — mirror the slog stream

`slog` handlers are wrapped so every record goes to **stdout and the boot
file** (`io.MultiWriter`; the existing per-module loggers and debug gating are
untouched — persistence sees exactly what stdout sees, JSON ndjson format).

- Rotation: when a boot file reaches `log-supervisor-rotation-bytes`, roll to
  `boot-<ts>-<bootid8>.<n>.ndjson`. Only the current boot's files are ever
  written; older boots are immutable.
- Crash safety: append + OS buffering, no per-line fsync. A hard crash may
  lose the last few lines — acceptable for forensics, and the DB/backups
  remain the source of truth for state.
- Multi-process safety is not a concern: only one supervisor instance writes
  (single-instance contract, same as the DB).

### 5.2 Runner log capture — capture at the removal choke point

The three scattered `ContainerRemove` call sites in
`internal/orchestrator/docker` collapse into one internal
**`removeRunner(ctx, ...)` choke point** that, in order:

1. **Captures** the container's stdout via the Docker log API — tail-capped at
   `log-runner-capture-max-bytes` (keep the *end*: the beginning is bootstrap
   noise, the end is the failure), hard timeout
   `log-runner-capture-timeout-seconds`, best-effort: any error is recorded in
   the removal record (`capture.ok = false`, error string) and **never blocks
   or delays removal** beyond the timeout.
2. **Removes** the container (existing force-remove semantics).
3. **Appends the removal record** to `removals.jsonl` and echoes a one-line
   summary to the supervisor stream.

Capture-before-remove covers the normal paths (drain, idle-reap, job
completion, create-failure rollback, orphan sweep).

**Close-time capture for job rows (RUN-252).** Capture-at-removal alone left
a UI gap: a warm idle runner serves many jobs before it is ever removed, so
a job that completed normally had no capture to point at. The busy→idle
close now snapshots the container's logs (same tail cap and hard timeout,
best-effort per docs/21 G3) and records the path on the job row; the die-event
reap threads its removal-capture path into the closed row the same way. A row
without a path is still recoverable: the GetRunnerLogs resolver falls back to
the removals journal (docs/29 §5.2).

**Die-event echo suppression (RUN-234).** Every supervisor-initiated removal
marks the container id; when Docker's `die` event for that id arrives, the
reap path recognizes the echo and skips capture and record — recording
again would emit a *guaranteed-failed* capture (we removed the container
ourselves). This matters for forensics: without suppression, every drain
produced a paired `reap` record with a failed capture, and genuine capture
losses drowned in noise (during the 2026-09-16 churn, 248 of 255 reap
records were such echoes). A reap record with a failed capture now means
exactly one thing: *someone other than this supervisor removed the
container before its logs could be captured* — the signal worth paging on.

### 5.3 The recreation window — boot-time reconciliation

"Runner died while the supervisor was being recreated" is covered because the
corpse lingers (§3): the **boot-time reconciliation sweep** — which already
must classify unknown/leftover runner containers on startup — routes every
removal it performs through the same `removeRunner` choke point. A supervisor
that comes up after any downtime therefore captures and records what it
removes, closing the window without any long-lived capture process.

### 5.4 Removal decision records

`removals.jsonl` lines are small, self-describing JSON objects:

```json
{"ts":"...","boot_id":"...","runner_id":"...","provider":"github",
 "pool_id":"...","container":"...","reason":"drain|idle-reap|orphan-sweep|
 create-failure|job-complete|...","provider_busy":false,
 "veto":{"requested":"deregister","outcome":"approved"},
 "capture":{"ok":true,"bytes":48213,"file":"runners/github-12-77-....log"}}
```

This is the greppable artifact the incident asked for: one file, one line per
removal, retention-capped like everything else.

### 5.5 Retention & disk budget

A retention sweeper runs **at boot** and then **hourly** via the existing
`internal/cron` scheduler:

| Knob (koanf key) | Default | Meaning |
| --- | --- | --- |
| `log-persistence-enabled` | `true` | kill switch; stdout behavior unchanged |
| `log-supervisor-rotation-bytes` | 32 MiB | per boot-file rotation threshold |
| `log-supervisor-max-files` | 10 | oldest boot files deleted first |
| `log-runner-capture-max-bytes` | 16 MiB | per-runner tail cap |
| `log-runner-max-files` | 200 | oldest captures deleted first |
| `log-total-budget-bytes` | 1 GiB | hard guard; sweeper enforces LRU across all of `/data/logs` |

All keys follow the existing flat koanf style (`data-dir`,
`db-encryption-key`, …): YAML/TOML/env/CLI spelled identically, `Default*`
constants in `internal/config`. The budget exists to make disk exhaustion
impossible even with hostile job output; normal operation should sit far
below it.

### 5.6 Why not streaming capture

A `docker logs --follow` goroutine per runner was considered and rejected:

- It lives **inside the supervisor process**, so it dies at exactly the moment
  the owner's constraint says must be tolerated (recreation) — and it would
  still need removal-time capture as the backstop for the restart gap. Its
  complexity would be paid twice for partial coverage.
- Since dead runner containers linger, capture-at-removal plus boot-time
  reconciliation already observes every byte that ever reached the container's
  json-file log, without a per-runner goroutine, back-pressure handling, or
  partial-file recovery logic.
- Should streaming ever become necessary (e.g. live-tailing in the UI,
  RUN-213), the on-disk formats above are already line-oriented and would not
  change.

Central logging drivers / external collectors were also rejected: new moving
parts on the shared Docker host for a file-based acceptance (§7).

## 6. Protocol changes

None. No RPC/proto/web changes; the web UI is untouched (RUN-213 owns any
future exposure).

## 7. Security implications

- **Durable secrets surface.** Persisted logs share the lifetime and trust
  domain of `/data` (same volume as the AES-encrypted DB). Supervisor ndjson
  inherits the existing no-secrets-in-logs discipline (tokens, keys, and
  credentials are never logged — RUN-9/RUN-26 conventions). Runner captures
  are *raw agent stdout*: the runner image's zero-leak policy (no PATs or
  registration tokens echoed) is what keeps them clean, and `0600`/`0700`
  modes bound file access to the supervisor's user.
- **Disk-exhaustion guard.** Runner stdout is untrusted job-adjacent output;
  the byte caps and `log-total-budget-bytes` make a noisy or malicious runner
  unable to fill the volume (which would take down the DB).
- **Ghost-runners forensics.** Removal records make RUN-182-class vetoes and
  deregistrations auditable after the fact, without log archaeology.

## 8. Testing & verification

- **Unit:** sink rotation/retention; boot-file separation across simulated
  restarts; choke-point capture via the existing Docker mock (success, log-API
  failure, timeout paths — removal always proceeds); removal-record shape;
  config defaults and knob plumbing; boot-time reconciliation routes through
  the choke point.
- **Race:** `make test-race` on the orchestrator package (capture runs on
  drain/reap paths that already race-test).
- **Human checks for the implementation PR:** recreate the supervisor
  (`make restart`) mid-idle → old boot's `boot-*.ndjson` still present and the
  new boot starts a new file; kill a runner container manually → reconciliation
  captures its stdout and appends a removal record; `removals.jsonl` answers
  "why was runner X removed" after several drain/reap cycles.
- Docs: this file is the design; the implementation PR updates §5 knobs to
  as-built if names shift and moves the feature from README Roadmap to
  Features.

## 9. Acceptance (from RUN-186)

After a supervisor recreation and a runner drain, an operator can answer
"what happened to runner X in its last job / why was it removed" from files
that survived the recreation.

## 10. As-built notes (implementation PR)

The implementation follows this design with four deliberate adjustments,
each preserving the acceptance:

- **Runner captures reuse the existing compressed JSONL store.** M25
  (job history) already persists runner stdout as
  `<data-dir>/logs/<runner-id>.log.jsonl.gz` and consumers (job rows,
  Renovate task logs) key on that location, so the choke point captures
  into the same store instead of introducing a parallel raw-text
  `runners/` subtree. Durability is identical; one capture format, not two.
- **The removal choke point lives in the pool controller**, not the Docker
  wrapper: `terminateAndRecord` (capture → terminate → untrack → record)
  is shared by the reap, lifetime-limit, idle-drain, shutdown, pool-drain,
  recycle, manual-API, and task-exit paths, and spawn failures are recorded
  as `create-failure` by the spawn pipeline. The Docker client's internal
  rollback (start-failure) stays engine-side since the controller observes
  the resulting error.
- **Boot-time reconciliation needs no new sweep**: the audit cycle already
  reaps exited containers (including leftovers from a previous supervisor
  lifetime) through `reapContainer`, which now flows through the choke
  point — the recreation window closes on the first reconcile pass.
- **The capture timeout is enforced with `context.WithTimeout`** around
  the capture call (`log-runner-capture-timeout-seconds`, default 10 s),
  and the retention sweeper runs in the daemon (boot + hourly) over
  `logs/supervisor/` and `logs/` with the total budget across both.

`removals.jsonl` field names as shipped: `ts`, `boot_id`, `runner_id`,
`runner_name`, `pool_id`, `pool_name`, `container`, `reason` (`reap`,
`task-exit`, `lifetime-limit`, `idle-drain`, `shutdown`, `pool-drain`,
`recycle`, `manual`, `create-failure`), `provider_busy`, `dereg_error`,
`exit_code`, and `capture` (`ok`, `bytes`, `file`, `error`, `skipped`).
