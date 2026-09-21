# 🔍 Full Codebase Review — Findings Report

> **Project**: `noosxe/runnero` — Self-Hosted GitHub/Gitea/Forgejo Actions Runner Supervisor
> **Original review**: 2026-09-07
> **Re-verified**: 2026-09-21 against `main @ e8235b1`
> **Scope**: All source code, documentation, infrastructure, CI/CD, frontend, and protobuf definitions

> [!NOTE]
> **2026-09-21 re-verification.** Every finding below was re-checked against the
> current tree. Status banners mark each item: ✅ **RESOLVED** (fixed or invalid
> by design), 🟦 **WAIVED — WORKING AS DESIGNED** (still true, but a documented,
> deliberate product decision), ⚠️ **STILL VALID** (actionable), and 🔶
> **PARTIALLY RESOLVED**. Line numbers and evidence have been updated to the
> current tree. Severity shown is the **original** rating; re-rated severity is
> noted where the risk has changed.

---

## Summary

| Category | Findings | ✅ Resolved | 🟦 Waived / WAD | ⚠️ Still valid | 🔶 Partial |
|---|:---:|:---:|:---:|:---:|:---:|
| Security | 6 | 3 | 2 | 1 | 0 |
| Missing Functionality | 4 | 1 | 0 | 3 | 0 |
| Design ↔ Implementation Gaps | 4 | 1 | 1 | 2 | 0 |
| Code Quality | 5 | 0 | 0 | 5 | 0 |
| Infrastructure / Docker | 2 | 0 | 0 | 2 | 0 |
| Testing Gaps | 3 | 0 | 0 | 2 | 1 |
| **Total** | **24** | **5** | **3** | **15** | **1** |

### Remaining actionable work, by priority

1. **INFRA-01** — socket security note referenced by `docker-compose.yml` does not exist in the README (stale pointer); no socket-proxy / `:ro` guidance anywhere.
2. **MISS-02** — image-update notifications are in-memory only *and* undocumented as intentional.
3. **MISS-04** — `cap_drop` hardening is required by AGENTS.md but documented nowhere; the one in-repo reference points at a docs section that doesn't cover it.
4. **SEC-05** — `/actions-runner` layer bloat (`COPY` without `--chown` + separate `chown -R`).
5. **TEST-02** — `entrypoint.sh` is never exercised against a real container daemon by any gate.
6. **INFRA-02** — standalone runner service has no healthcheck.
7. **GAP-02** — `install-tools.sh`: no strict mode, unpinned tools (increasingly vestigial next to the Nix dev shell).

---

## ✅ Resolved Issues

### SEC-01: Supervisor Container Runs as Root — ✅ RESOLVED

**File**: [Dockerfile.supervisor](Dockerfile.supervisor)
**Original severity**: Critical

Fixed. The final stage now creates a dedicated non-root user —
`addgroup -g 10000 supervisor && adduser -u 10000 -G supervisor -D …` (explicitly
"per docs/05-security-and-isolation.md") — an s6 init service
(`deploy/supervisor/s6-rc.d/init-socket-perms/`) reads the mounted
`/var/run/docker.sock` GID at boot and adapts group membership, and the daemon
run script execs via `s6-setuidgid supervisor`. The supervisor daemon itself no
longer runs as root.

Residual (accepted): s6-overlay's brief init phase runs as root — inherent to
the s6-overlay bootstrap model, needed for the socket-perm adaptation and
setuid drop. Rootless-Docker/Podman alternatives are tracked as *Future
Mitigation* in docs/05 §4.

---

### SEC-03: No Server-Side Logout / Session Revocation RPC — ✅ RESOLVED

**File**: [api.proto](proto/api.proto)
**Original severity**: High

Fixed (RUN-229, docs/32). `AuthService` now defines:

- `rpc Logout` — deletes the caller's session row and clears the cookie (proto line 38);
- `rpc RevokeSession` — per-row revoke, current-row revoke behaves like Logout;
- `rpc RevokeAllOtherSessions`.

Sessions are opaque DB-backed tokens with device labels and idle/absolute
clocks; the Security tab exposes all of it (E2E spec `10-session-control.spec.ts`).

---

### SEC-04: No `AuthProfileService.UpdateAuthProfile` RPC — ✅ RESOLVED

**File**: [api.proto](proto/api.proto#L510)
**Original severity**: High

Fixed. `rpc UpdateAuthProfile(UpdateAuthProfileRequest)` exists (proto line 510),
connected to the DB layer's re-encryption path. Token/PAT rotation no longer
requires delete + re-create.

---

### MISS-03: No Custom Network in Docker Compose — ✅ RESOLVED (invalid by design)

**Files**: [docker-compose.yml](docker-compose.yml), [provider.go](internal/orchestrator/provider.go#L12)
**Original severity**: Medium

Not an issue. The orchestrator creates and manages the `runnero-supervisor`
bridge itself (`DefaultNetworkName`, `EnsureNetwork` — OQ #22); compose doesn't
need to pre-define it. Runner containers never call the supervisor directly —
they register with the provider — so there is no supervisor↔runner same-network
requirement to satisfy.

---

### GAP-03: AGENTS.md References Non-Existent `register.sh` — ✅ RESOLVED

**File**: [AGENTS.md](AGENTS.md)
**Original severity**: Low

Fixed. AGENTS.md no longer references `register.sh`. (The docs/02 reference
survives — see GAP-04 below.)

---

## 🟦 Waived — Working as Designed

### SEC-02: Runner Container Has Passwordless Sudo — 🟦 WAIVED (documented decision)

**File**: [Dockerfile](Dockerfile#L138-L143)
**Original severity**: Critical

Still true — `runner ALL=(ALL:ALL) NOPASSWD:ALL` remains — but it is now an
explicitly documented product decision, not an oversight:

- docs/05 records the rationale (hosted-runner contract: community workflows
  call `sudo apt-get install …` in setup steps), the trust-boundary analysis
  (sudo widens only *in-container* blast radius; pool trust is the admin's
  decision), the ephemerality argument, and the deployment caveat
  (`no-new-privileges` must stay off the runner service).
- The sudoers drop-ins are `visudo -c`-validated at build time, with an
  `env_keep` rule preserving `RUNNER_*`/provider env across the entrypoint's
  sudo re-exec (docs/18 §3.7, RUN-162).

The original recommendation (blanket-removal / command allowlist) conflicts
with the documented workflow-parity requirement and is **rejected**. Sudo
cannot grant capabilities outside the container's bounding set.

---

### SEC-06: `VACUUM INTO` String Formatting — 🟦 WAIVED (accept, document)

**File**: [backup.go](internal/db/backup.go#L55-L56)
**Original severity**: Medium → effectively Low

Unchanged: `query := fmt.Sprintf("VACUUM INTO '%s';", escaped)` with
`strings.ReplaceAll(destPath, "'", "''")` escaping. The sole caller is the
internal backup manager (`backup.go:76`); `destPath` derives from server config
(data dir + generated filename), never from user input. SQLite's `VACUUM INTO`
target can be parameterized in principle, but the practical risk here is nil.

Remaining nit: no comment at the call site states that `destPath` is
internally controlled. A one-line comment would close this out.

---

### GAP-01: CORS Strict Denial With No Configuration Knob — 🟦 WAIVED (intentional, but undocumented)

**File**: [server.go](internal/server/server.go#L482)
**Original severity**: Medium → Low

Still factually true: CORS is strictly denied (same-origin only, OQ #25/#26)
with a webhook-receiver bypass, and there is no `SUPERVISOR_CORS_*` env var.
This is the intended security posture — the deployment story is reverse-proxy
TLS termination on one origin (README, docs/05) — so adding a cross-origin
escape hatch is not wanted.

The legitimate residue is documentation: the same-origin policy lives only in
code comments. It should be stated in docs/05 (or docs/08).

---

## ⚠️ Still Valid Issues

### INFRA-01: Docker Socket Mounted Read-Write; Security Guidance Stale — ⚠️ STILL VALID (sharpened)

**Files**: [docker-compose.yml](docker-compose.yml), README.md, docs/05 §4
**Original severity**: Critical → Medium (DooD is architecturally required; docs/05 §4 covers the trust boundary)

Current state:

- The compose comment on the supervisor's socket mount says *"see security note
  in README"* — **the README contains no docker.sock security note at all**.
  The pointer is stale (or was never written).
- docs/05 §4 ("Docker Socket Isolation (DooD Safety)") documents the
  runner-side boundary (socket not mounted into runner containers by default,
  GitHub-only opt-in, rootless/Podman deferred) — but says nothing about the
  **supervisor's** full-rw host socket mount, socket proxies
  (e.g. Tecnativa/docker-socket-proxy), or `:ro` options.
- The standalone `runner` service still mounts the socket rw with no comment.

**Recommendation**: write the README security note the compose comment already
promises (socket exposure rationale + socket-proxy mention), and align the
standalone runner's mount comment.

---

### INFRA-02: Standalone Runner Service Missing Health Check — ⚠️ STILL VALID

**File**: [docker-compose.yml](docker-compose.yml) (`runner` service)
**Original severity**: High → Medium (optional `runner-standalone` profile; the supervisor-managed path is the primary deployment)

Unchanged: the `runner` service has `init: true` and `restart: unless-stopped`
but no `healthcheck`. Docker/compose cannot detect a wedged runner process.

**Recommendation**: a `CMD-SHELL` check on the runner process or a sentinel
file written by `entrypoint.sh`.

---

### SEC-05: Dockerfile Layer Bloat From Separate `chown` — ⚠️ STILL VALID

**File**: [Dockerfile](Dockerfile#L152-L158) (lines 152–158)
**Original severity**: Medium

Unchanged: `COPY --from=downloader /build/actions-runner /actions-runner`
(no `--chown`) is followed by a separate `RUN` that does
`chown -R 1001:1001 /actions-runner` (alongside `installdependencies.sh`),
duplicating the ~300–500 MB directory in layers. The sibling runner binaries
right below (lines 150–151) correctly use `COPY --chown=root:root --chmod=755`.

**Recommendation**: `COPY --from=downloader --chown=1001:1001
/build/actions-runner /actions-runner` and run `installdependencies.sh` in its
own layer.

---

### MISS-01 / GAP-04: Docs Still Reference `register.sh` — ⚠️ STILL VALID (downgraded to docs/02 only)

**File**: [02-architecture-design.md](docs/02-architecture-design.md#L91)
**Original severity**: High (MISS-01) / Low (GAP-04)

AGENTS.md is fixed; **docs/02 line 91 still lists `register.sh`** in the
repository structure ("entrypoint.sh, register.sh"). The file doesn't exist —
registration logic is inlined in `entrypoint.sh`. One-line docs fix; MISS-01's
"High" rating no longer applies since only a doc tree-diagram is wrong.

---

### MISS-02: `ImageUpdateService` Has No Persistent Store — ⚠️ STILL VALID

**Files**: [api.proto](proto/api.proto#L902), [image_update.go](internal/server/image_update.go#L88-L98), `internal/db/migrations/` (through 012)
**Original severity**: Medium

Unchanged: the service keeps notifications in a `sync.RWMutex`-guarded
`map[int64]*supervisorv1.ImageUpdate`; no `image_updates` table exists in any
migration, and no doc marks the ephemerality as intentional. Consequences
stand: notifications and dismissals are lost on supervisor restart; no update
history.

**Recommendation**: either add a persistence migration (and prune on
pull/dismiss) or document the in-memory design choice where the service is
described.

---

### MISS-04: Missing `cap_drop` Documentation — ⚠️ STILL VALID (sharpened: dangling reference)

**Files**: AGENTS.md, docker-compose.yml, docs/05
**Original severity**: Medium

AGENTS.md still requires documenting minimal Docker configurations
(`cap_drop`), and neither the compose file nor the README carries any.
**New wrinkle**: the only in-repo mention — docs/05 line 80,
"`cap_drop` hardening (§4 guidance) remains fully effective" — points at §4,
which is *Docker Socket Isolation* and contains **no cap_drop guidance
whatsoever**. The reference is dangling.

**Recommendation**: add a short hardening subsection (suggested
`cap_drop: [ALL]` + `cap_add` allowlist per service, and the
`no-new-privileges` runner caveat that docs/05 *does* already cover) and fix
the §4 cross-reference.

---

### GAP-02: `install-tools.sh` Missing Strict Mode; Tools Unpinned — ⚠️ STILL VALID

**File**: [install-tools.sh](scripts/install-tools.sh#L3)
**Original severity**: Medium → Low

Unchanged: `set -e` only, every tool installed `@latest`. Context has shifted,
though: the Nix dev shell is now the canonical toolchain (AGENTS.md mandates
it), so this script is increasingly vestigial — which argues for pinning or
retiring it rather than investing in it.

---

### TEST-01: Missing E2E for Image-Update Workflows — 🔶 PARTIALLY RESOLVED

**Files**: [tests/e2e/specs/](tests/e2e/specs/)
**Original severity**: Medium

The job-history half is done: `14-job-history-flow.spec.ts` covers history
navigation and filtering. Still missing: an E2E spec exercising image-update
notification/dismissal (the settings "Pending Image Notifications" section has
no E2E coverage). The E2E stack's mock docker daemon would need to emulate the
inspect/pull surface — same machinery the suite already fakes for spawns.

---

### TEST-02: No Integration Test Exercising the Entrypoint Against a Real Daemon — ⚠️ STILL VALID

**Files**: `tests/unit/` (3 shell unit suites), [Makefile](Makefile)
**Original severity**: Medium

Unchanged: `make test-scripts` runs `entrypoint_test.sh`,
`parity_packages_test.sh`, and `playwright_lockstep_test.sh` — all unit-level.
The Playwright suite uses a **mock** docker daemon, so `entrypoint.sh`
(registration, re-exec, graceful deregistration) is never executed against a
real container daemon anywhere in CI. The standalone-runner compose profile
would be the natural vehicle for a smoke-level integration test.

---

### TEST-03: No Dedicated Vitest Config File — ⚠️ STILL VALID (cosmetic)

**File**: [vite.config.ts](web/vite.config.ts)
**Original severity**: Low

Unchanged: Vitest config remains inline in `vite.config.ts` (test block with
jsdom environment and setup files). Works fine; a `vitest.config.ts` split
remains a nice-to-have.

---

### QUAL-01: `resp.Body.Close()` Errors Silently Ignored in Registry Client — ⚠️ STILL VALID

**File**: [internal/registry/client.go](internal/registry/client.go) (lines 155, 177, 206, 267)
**Original severity**: Low

Unchanged: four `defer func() { _ = resp.Body.Close() }()` sites. Idiomatic Go;
response bodies on GETs carry nothing worth reading on close. Observability nit
only.

---

### QUAL-02: s6 `run` Script Lacks Strict Mode — ⚠️ STILL VALID (cosmetic)

**File**: [deploy/supervisor/s6-rc.d/supervisor/run](deploy/supervisor/s6-rc.d/supervisor/run)
**Original severity**: Low

Still a bare `#!/command/with-contenv sh` + single `exec s6-setuidgid
supervisor …` line — no `set -euo pipefail`. The script is now a one-line exec
chain (and gained the important part: the setuid drop, per SEC-01), so strict
mode has nothing to guard. Fine to leave as is; noting for completeness.

---

### QUAL-03: `apk del xz tar` in Supervisor Dockerfile — ⚠️ STILL VALID (cosmetic)

**File**: [Dockerfile.supervisor](Dockerfile.supervisor#L88)
**Original severity**: Low

Unchanged: `xz tar` are installed with `--no-cache` (line 58) and removed with
`apk del` (line 88) in a later layer. Unlike cache cleaning, this genuinely
shrinks the image (the packages themselves are deleted); it just re-adds them
to the earlier layer's size. Purely stylistic.

---

### QUAL-04: Runner Base Image Is Full `ubuntu:24.04` — ⚠️ STILL VALID (partially mitigated)

**File**: [Dockerfile](Dockerfile#L7,L66)
**Original severity**: Low

Unchanged: both stages use full `ubuntu:24.04`. The choice is defensible — the
GitHub Actions runner and its dependency stack target Ubuntu, and
docs/18 documents the package-parity tiering — but the base-image decision
itself (why not a slimmer base for the downloader stage, why full Ubuntu for
the runtime) still isn't stated explicitly. A sentence in the Dockerfile header
comment or docs/18 would close it.

---

### QUAL-05: `runner_pools.repository_url` Retained Alongside `pool_targets` — ⚠️ STILL VALID (nuanced)

**Files**: [001_initial_schema.sql](internal/db/migrations/001_initial_schema.sql#L36), [server/pool.go](internal/server/pool.go#L220-L287), migrations 003+
**Original severity**: Low

Unchanged in substance: migration 003 backfilled `pool_targets` from
`runner_pools.repository_url` but never dropped the legacy column — and unlike
the original report assumed, the column is not dead weight: the write path
still maintains it (create/update set it, audit diffs it — `pool.go` 249/287)
and reads it as a fallback when a pool has no targets (`pool.go` 220–221). So
it's a live, synced duplicate of the first target.

**Recommendation**: either drop it in a follow-up migration (collapsing the
fallback onto `pool_targets`) or document why the mirror is kept.

---

## ✅ What's Working Well

These areas are implemented cleanly and match the design docs (re-confirmed 2026-09-21):

| Area | Assessment |
|---|---|
| **Go Backend Architecture** | Modular, well-tested, zero TODOs/FIXMEs. Strict DI, context propagation, and slog logging. |
| **ConnectRPC Services** | All services fully implemented with proper binary wire protocol enforcement; session lifecycle (login/logout/revoke) is complete. |
| **Database Layer** | sqlc-generated queries, Goose migrations (through 012), AES-256 encryption, HKDF key derivation — all solid. |
| **Orchestrator** | Complete lifecycle management with scale-to-zero, ephemeral containers, webhook scaling, busy-sync, ghost sweep, and graceful shutdown. |
| **Frontend** | React 19 + TypeScript + TanStack Router/Query + TailwindCSS. Strict typing, no `@ts-ignore`; WCAG 2.2 AA enforced by test gates (RUN-255). |
| **Security Headers & Auth** | Opaque HttpOnly/SameSite=Strict session cookies, login lockout, passkeys (WebAuthn), security middleware, CORS denial, audit logging. |
| **CI/CD Pipelines** | Workflow files covering build, test, lint, and deploy. Path-based filtering with Dependabot. |
| **Entrypoint Script** | Fully compliant: `set -euo pipefail`, signal traps, graceful deregistration, sudo re-exec with env preservation. |
| **Health Probes** | `/healthz` (liveness) and `/readyz` (readiness) with pluggable probe registries. |
| **Backup & Recovery** | VACUUM INTO snapshots, configurable interval and retention. |
| **Proto Design** | Clean, consistent naming. Secrets never exposed in read RPCs. |
| **Test Coverage** | Exceptionally thorough Go tests, 16-containerized E2E specs (bootstrap → viewer role → a11y enforcement), shell unit suites, and a Vitest axe gate. |
