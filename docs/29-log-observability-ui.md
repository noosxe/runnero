# Web Log Observability — Supervisor & Runner Log Viewer (RUN-213)

Status: design — RUN-213. Builds on the on-disk artifacts from RUN-186
(docs/28); that design's §6 explicitly deferred all RPC/web exposure to
this document.

## 1. Summary

Surface the persisted log artifacts under `<data-dir>/logs/` in the web UI,
so an operator can answer **"what happened to runner X in its last job, and
why was it removed"** — and inspect what the supervisor itself did across
boots — without shell access to the host.

Three exposures, delivered as one feature (backend + UI in one design,
split across two implementation PRs):

1. **Supervisor boot log viewer** — browse boot-separated ndjson files
   (`logs/supervisor/boot-*.ndjson`), tail the current boot live.
2. **Removal decision records browser** — filterable table over
   `logs/removals.jsonl` with one-click navigation to the runner's
   captured stdout.
3. **Runner capture viewer hardening** — the existing runner-log RPCs gain
   response caps and input validation, and every removal record / job
   history row links into them.

## 2. Motivation

RUN-186's acceptance is deliberately file-based: after a supervisor
recreation the operator greps `/data/logs`. That works on a laptop and
fails in practice everywhere else — the UI is the operator's primary
surface, and the incident class RUN-186 was built for (ghost runners,
mid-job deaths, veto outcomes) is exactly the class where the operator is
staring at the web UI, not a shell. The data is already durable and
retention-capped on disk; surfacing it adds read paths only.

## 3. Current state (as-built inventory)

Everything the UI needs already exists on disk and in code:

| Artifact | Location | Format | Writer |
| --- | --- | --- | --- |
| Supervisor boot logs | `logs/supervisor/boot-<unix>-<bootid8>[.<n>].ndjson` | slog JSON lines; first line `{boot_id, started_at, version}` | `logging.BootFileSink` (docs/28 §5.1) |
| Runner stdout captures | `logs/<container-id>.log.jsonl.gz` — keyed by Docker container ID; reads accept the container name via job-history resolution (RUN-252) | gzipped JSONL `{timestamp, stream, content}` | `orchestrator.CaptureAndCompressLogs` (M25 store, shared) |
| Removal records | `logs/removals.jsonl` | one `RemovalRecord` JSON object per line | `orchestrator.RemovalLogger` (docs/28 §5.4) |
| Retention sweeper | — | boot + hourly, LRU across all of `logs/`, 1 GiB budget | `logging.SweepLogs` |

RPC surface today (`LogService`, `internal/server/log.go`):

- `StreamRunnerLogs(runner_id)` — live `docker logs --follow` on a
  **running** container (multiplexed-framing aware); Docker resolves
  container name or ID, so both work live.
- `GetRunnerLogs(runner_id)` — reads the capture file by runner id
  (container ID). Accepts a container **name** too (RUN-252): when no
  capture exists under the exact id, the name resolves in two stages —
  (1) `job_history.log_retention_path` (latest closed row, newest first),
  recorded at close-time/removal capture; (2) the removals journal, latest
  removal of the name with a successful capture. Both stages reduce the
  stored path to its base name and re-validate it with the same
  `safeLogResourceName` rule, so a resolved lookup can only ever land on
  another file inside `logs/`. Best-effort: an unresolvable name keeps
  the not-found error.

Web today: `LogTerminal` renders both on `history-detail` (running vs
completed jobs) and `pool-detail` (live tail). The captures/removals/boot
files themselves have **no** listing UI, and removal records have no
protocol exposure at all.

## 4. Requirements

- **Functional**
  - List supervisor boot files with orientation metadata (boot id, boot
    time, size, current-boot badge); read any retained boot file; tail
    the current boot live.
  - Browse removal records with filters (pool, runner, reason, time
    range) and pagination; jump from a record to its captured stdout.
  - Look up a runner capture by runner id from anywhere (removal rows,
    job history), not only from a job that still exists in the DB.
- **Non-functional**
  - **Retention/permission parity:** the UI shows exactly what the files
    show — no separate retention, no cached copies; list endpoints read
    from disk so swept files disappear from the UI immediately.
  - **Bounded responses:** every read is line/byte-capped server-side;
    a 16 MiB capture must not become a 16 MiB JSON response.
  - **Same auth domain:** all new RPCs ride the existing session-cookie
    Connect auth; no new credentials or tokens.
  - **Graceful degradation:** absent/rotated/malformed artifacts yield
    empty results or skipped lines, never 500s.

## 5. Design

### 5.1 Protocol — extend `LogService`, additively

No new service, no changes to existing messages. Four additions to
`proto/supervisor/v1/api.proto` (docs/08 updated in the implementation PR):

```proto
// Inside service LogService:
// List retained supervisor boot files (orientation metadata only).
rpc ListSupervisorLogs (ListSupervisorLogsRequest) returns (ListSupervisorLogsResponse);
// Server-stream one boot file (ndjson lines re-emitted as log entries),
// optionally live-following the current boot.
rpc StreamSupervisorLog (StreamSupervisorLogRequest) returns (stream LogChunk);
// Paginated, filtered removal records (reverse-chronological).
rpc ListRemovalRecords (ListRemovalRecordsRequest) returns (ListRemovalRecordsResponse);

message SupervisorBootLog {
  string file = 1;        // base name only, e.g. "boot-1737000000-9f2c11ab.ndjson"
  string boot_id = 2;
  string started_at = 3;  // RFC3339; from the file header, filename fallback
  int64 size_bytes = 4;
  int32 rotation_seq = 5; // 0 for the unrotated file
  bool is_current = 6;    // belongs to this supervisor process's boot
}
message ListSupervisorLogsRequest {}
message ListSupervisorLogsResponse { repeated SupervisorBootLog boots = 1; }

message StreamSupervisorLogRequest {
  string file = 1;         // base name from ListSupervisorLogs
  bool follow = 2;         // only honored (and only meaningful) for the current boot
  int32 tail_lines = 3;    // replay last N lines before following; default 500
}
// Reuses LogChunk: timestamp from the ndjson record, stream = level
// ("INFO"/"WARN"/...), content = the slog message + attrs rendered as text.

message ListRemovalRecordsRequest {
  int64 pool_id = 1;       // optional filters; zero/empty = unset
  string runner_id = 2;
  string reason = 3;
  string since = 4;        // RFC3339
  string until = 5;
  int32 page_size = 6;     // default 50, max 200
  string cursor = 7;       // opaque: ts of the last record of the previous page
}
message RemovalRecordSummary {
  string ts = 1;           // RFC3339
  string boot_id = 2;
  string runner_id = 3;
  string runner_name = 4;
  int64 pool_id = 5;
  string pool_name = 6;
  string reason = 7;       // reap | task-exit | lifetime-limit | idle-drain | ...
  bool provider_busy = 8;
  string dereg_error = 9;
  int32 exit_code = 10;    // -1 sentinel = unset (proto has no optional int on this path)
  bool capture_ok = 11;
  int64 capture_bytes = 12;
}
message ListRemovalRecordsResponse {
  repeated RemovalRecordSummary records = 1;
  string next_cursor = 2;  // empty = end of file reached
}
```

And one existing message gains a field (additive, wire-compatible):

```proto
message GetRunnerLogsRequest {
  string runner_id = 1;
  int32 tail_lines = 2;    // default 500, max 5000; 0 = default
}
```

Deliberate choices:

- **`GetRunnerLogs` keeps its shape but gains `tail_lines`** (default
  500, max 5000, server-decoded from the end) — today it unmarshals the
  entire gzip into one response; with the 16 MiB per-runner cap that is
  an unbounded memory/amplification path once it is one click away.
- **Removal-record → capture navigation keys on `runner_id`**, never on a
  client-supplied file path: capture filenames are
  `<runner-id>.log.jsonl.gz`, so the record already carries the lookup
  key. Accepting paths would be a traversal liability for zero benefit.
- **`StreamSupervisorLog` reuses `LogChunk`** with the level in
  `stream` — the ndjson lines are already `{time, level, msg, ...}`;
  the server renders attrs as `key=value` text. No new message type.
- **Cursor pagination over `removals.jsonl`** is a reverse scan with a
  per-request **scan cap** (last 10k lines max) so a pathological file
  cannot turn one request into unbounded IO. `ts` equality collisions
  fall back to skipping forward until past the cursor line.

### 5.2 Server wiring

New `logs.go`-style handler code lives with the existing `LogService`
(`internal/server/log.go`, split into `log_supervisor.go` /
`log_removals.go` if it outgrows one file). Dependencies it needs are
already injected or derivable: `dataDir` (files), the `BootFileSink`'s
current base name + `is-current` answer (a tiny interface, mirroring the
existing `LogStreamer` seam so tests stay mock-based).

Reading rules, shared by all three endpoints:

- **Filename validation:** every client-supplied `file`/`runner_id` is
  matched against `^[A-Za-z0-9][A-Za-z0-9_.-]{0,128}$` and joined via
  `filepath.Base` before it can touch the filesystem — closes the
  path-traversal hole `GetRunnerLogs` has today (a runner id containing
  `../` currently escapes `logs/`; low severity — auth-gated, self-DoS —
  but this feature turns it into a listing-driven attack surface).
- **Directory listing is stat-only** (name, size, mtime); the boot
  header is read lazily per entry with a 1-line cap.
- **Malformed lines are skipped** (ndjson tolerance mirrors the
  sweeper's), capped with a `skipped_lines` counter only when large.

### 5.3 Web UI

New top-level route **`/logs`** (nav item "Logs", same guard set as the
other routes) with three tabs, reusing the existing `LogTerminal` for all
log rendering:

1. **Supervisor** — table of boot files (boot time, short boot id, size,
   rotation seq, `current` badge), newest first. Click → viewer with the
   last 500 lines; **Follow** toggle only for the current boot (server
   rejects `follow` on immutable files). This is also, incidentally, the
   first in-UI way to see the supervisor's own structured log stream.
2. **Removals** — filter bar (pool, runner id, reason select, time
   range) over `ListRemovalRecords`, infinite-scroll via cursor. Each
   row: time, pool, runner, reason badge (color-coded, same palette as
   pool diagnostics), busy flag, exit code, capture outcome. Row actions:
   **View capture** → runner tab prefilled with the record's runner id;
   **View boot** → supervisor tab pinned to that `boot_id`'s files.
3. **Runners** — lookup by runner id (free-text, prefilled via
   `/logs/runners?runner=<id>` deep links from removal rows and job
   history) → tail-capped capture in `LogTerminal`.

Client wiring follows the existing conventions: TanStack Query for the
list endpoints, the `streaming-hooks` pattern for
`StreamSupervisorLog`, structural `data-testid`s for E2E (no styling
classes as selectors — the #283 rule). No new client state machine: tabs
are path-driven (`/logs/<tab>` since RUN-283), which keeps deep links and browser
back/forward free.

### 5.4 Retention & permission parity

- The UI is a **live view of the files**: lists are directory reads, so
  the hourly/boot sweeper's deletions appear immediately; the UI adds no
  retention of its own and no second copy of the data.
- Access rides the existing session-cookie Connect auth — the same trust
  domain that already yields full admin over pools and runners. A UI
  reader could previously read these bytes via `docker exec`; nothing is
  being exposed to a *weaker* principal.
- File modes (`0600`/`0700`) bound host-level access exactly as in
  docs/28 §7; the supervisor process (the only reader) already owns them.

## 6. Security implications

- **No new secret surface.** The endpoints expose bytes the process
  already wrote; the zero-leak logging discipline (no PATs, registration
  tokens, or keys in logs — docs/28 §7) is unchanged. Runner captures
  remain raw agent stdout covered by the runner image's leak policy.
- **Path traversal is closed, not introduced.** `file`/`runner_id`
  validation (§5.2) is a security fix folded in because this feature
  makes the input surface list-driven and interactive.
- **DoS bounds.** Every response is capped (tail lines, page size, scan
  cap, 1-line header reads); gzip decompression is bounded by the
  retention caps on the files themselves. A malicious admin can at worst
  read their own logs — the auth model already grants full control.
- **Rendering safety.** Log content renders as text in `LogTerminal`
  (no HTML injection path); removal-record fields render as text/badges.
- **Audit.** Reads are read-only and non-mutating; no audit-log entries
  are required by the existing audit policy (which covers state
  changes), and log *reads* deliberately stay out of the logs to avoid
  feedback loops.

## 7. Testing & verification

- **Unit (server):** boot-file listing (header parse, filename fallback,
  rotated files, current badge), stream (tail replay, follow on current,
  follow rejected on old boot, malformed-line skip), removal listing
  (filters, cursor pagination, scan cap, ts-collision handling),
  `GetRunnerLogs` tail caps, traversal rejection (`../`, absolute paths,
  empty), zero-config defaults.
- **Web:** jsdom tests for the three tabs (list render, filter submit,
  deep-link prefill, capture-ok/badged rows); mock streaming per the
  existing `router-mock`/streaming test patterns.
- **E2E:** new spec seeded with a boot file + removal record + capture:
  navigate `/logs`, open a boot, filter a removal, jump record →
  capture. UI-facing change → `make test-e2e` is a merge gate.
- **Human checks (implementation PR):** recreate the supervisor → new
  boot appears with `current` badge, old boot still readable; drain a
  runner → removal row appears with capture intact; `Follow` on the
  current boot streams live output.

## 8. Sizing & delivery

**L overall.** Recommended split into two implementation issues (the
protocol boundary is the natural seam, and landing the backend first
unblocks UI review on a stable surface):

1. **Backend: log observability RPCs** (proto + server handlers +
   traversal fix + unit tests) — M.
2. **Web: logs page** (route, three tabs, streaming hook, tests, E2E
   spec, README Features flip) — M.

## 9. Alternatives considered

- **Sidecar file server / `docker exec` recipes:** bypasses the session
  auth model, new moving parts on the host, no UI. Rejected.
- **Index logs into SQLite:** queries get SQL ergonomics, but the files
  are the retention-capped record of truth; duplicating them into the DB
  doubles the disk budget story and couples log lifecycle to migrations.
  The jsonl scan with a 10k-line cap is fast enough at the scale the
  budget implies (≤ 1 GiB total). Rejected.
- **Download-only (presigned URLs / `rpc Download`):** minimal protocol
  work, but no forensics in the UI — the operator still ends up in a
  shell. Rejected as the primary UX; a "download file" affordance can be
  added later on the same endpoints.
- **New `LogArchiveService`:** service sprawl for three methods that all
  read the same directory tree; `LogService` is the cohesive home.
  Rejected.

## 10. Acceptance

From the RUN-213 issue: with no shell access, an operator opens the web
UI after a supervisor recreation and (a) reads the previous boot's
structured log, (b) finds the record of a runner removal including its
provider-busy/veto outcome, and (c) reads that runner's captured stdout —
all capped, authenticated, and reflecting the exact on-disk retention
state.
