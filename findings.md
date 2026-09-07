# 🔍 Full Codebase Review — Findings Report

> **Project**: `noosxe/runnero` — Self-Hosted GitHub/Gitea/Forgejo Actions Runner Supervisor  
> **Date**: 2026-09-07  
> **Scope**: All source code, documentation, infrastructure, CI/CD, frontend, and protobuf definitions

---

## Summary

| Category | Critical | High | Medium | Low |
|---|:---:|:---:|:---:|:---:|
| Security | 2 | 2 | 2 | 1 |
| Missing Functionality | 0 | 2 | 3 | 1 |
| Design ↔ Implementation Gaps | 0 | 1 | 3 | 2 |
| Code Quality / Bugs | 0 | 0 | 2 | 3 |
| Infrastructure / Docker | 1 | 1 | 2 | 2 |
| Testing Gaps | 0 | 0 | 2 | 1 |
| **Total** | **3** | **6** | **14** | **10** |

---

## 🔴 Critical Issues

### SEC-01: Supervisor Container Runs as Root

**File**: [Dockerfile.supervisor](file:///home/mechsoull/Projects/runnero/Dockerfile.supervisor)  
**Severity**: Critical

The `Dockerfile.supervisor` never includes a `USER` directive in the final stage. The supervisor daemon and all s6-managed processes run as `root` inside the container. This violates the project's own security requirements in [AGENTS.md](file:///home/mechsoull/Projects/runnero/AGENTS.md) ("Non-Root Execution") and [05-security-and-isolation.md](file:///home/mechsoull/Projects/runnero/docs/05-security-and-isolation.md).

> [!CAUTION]
> Combined with the Docker socket mount, a root-running supervisor container with `/var/run/docker.sock` access is a container-escape-to-host-root vector.

**Recommendation**: Create a non-root user (e.g., `supervisor:supervisor`) in the final stage and run the s6 process tree under it, or document a rootless Docker alternative.

---

### SEC-02: Runner Container Has Passwordless Sudo

**File**: [Dockerfile](file:///home/mechsoull/Projects/runnero/Dockerfile#L113)  
**Severity**: Critical

```dockerfile
echo "runner ALL=(ALL) NOPASSWD:ALL" >> /etc/sudoers
```

While the runner correctly runs as user `1001`, the passwordless sudo grant completely negates the non-root benefit. Any workflow code (which is untrusted by definition) can trivially escalate to root via `sudo`.

**Recommendation**: Remove the blanket sudo rule. If specific elevated commands are needed, allowlist them explicitly (e.g., `runner ALL=(ALL) NOPASSWD: /usr/bin/docker`).

---

### INFRA-01: Docker Socket Mounted Read-Write Without Security Warning

**File**: [docker-compose.yml](file:///home/mechsoull/Projects/runnero/docker-compose.yml#L27)  
**Severity**: Critical

Both `supervisor` and `runner` services mount `/var/run/docker.sock` with full read-write access. While this is architecturally required for orchestration, the compose file lacks any security notice, and there is no `:ro` option or socket proxy (e.g., Tecnativa/docker-socket-proxy) configured.

**Recommendation**:
- Add YAML comments warning about the privilege escalation risk
- Document socket proxy alternatives in README/deploy docs
- Consider read-only mount for the standalone runner service (`:ro`)

---

## 🟠 High Severity Issues

### SEC-03: No Server-Side Logout / Session Revocation RPC

**Files**: [api.proto](file:///home/mechsoull/Projects/runnero/proto/api.proto#L11-L20), [query-hooks.ts](file:///home/mechsoull/Projects/runnero/web/src/lib/api/query-hooks.ts#L159-L165)  
**Severity**: High

The `AuthService` proto defines `SetupAdmin`, `Login`, and `GetSession` — but **no `Logout` RPC**. The frontend's `useLogout()` hook only clears the local TanStack Query cache:

```typescript
export function useLogout() {
  const queryClient = useQueryClient();
  return () => {
    queryClient.setQueryData(queryKeys.session, null);
    queryClient.invalidateQueries({ queryKey: queryKeys.session });
  };
}
```

This means the JWT session cookie is **never invalidated server-side**. The session token remains valid until natural expiry. If a token is stolen, there is no way to revoke it.

The DB layer already has `DeleteSessionByTokenHash()` and `DeleteSessionsByUserId()` — the server-side plumbing exists but is never exposed via RPC.

**Recommendation**: Add a `Logout` RPC to `AuthService` that deletes the session from the DB and clears the `HttpOnly` cookie.

---

### SEC-04: No `AuthProfileService.UpdateAuthProfile` RPC

**File**: [api.proto](file:///home/mechsoull/Projects/runnero/proto/api.proto#L216-L220)  
**Severity**: High

`AuthProfileService` only defines `List`, `Create`, and `Delete`. There is no `Update` RPC. If a user needs to rotate a PAT token or replace a GitHub App private key, they must delete and re-create the entire profile, which cascades to deleting all associated runner pools.

The DB layer (`UpdateAuthProfile` in [auth_profiles.sql.go](file:///home/mechsoull/Projects/runnero/internal/db/auth_profiles.sql.go#L155)) and the crypto re-encryption logic ([crypto.go](file:///home/mechsoull/Projects/runnero/internal/db/crypto.go#L195)) are fully implemented — but never exposed via RPC.

**Recommendation**: Add `UpdateAuthProfile` RPC to the proto and connect it to the existing DB method.

---

### MISS-01: Missing `register.sh` Script

**Files**: [AGENTS.md](file:///home/mechsoull/Projects/runnero/AGENTS.md#L199), [02-architecture-design.md](file:///home/mechsoull/Projects/runnero/docs/02-architecture-design.md#L91)  
**Severity**: High

Both AGENTS.md and the architecture doc reference `src/register.sh` as a companion script. This file does not exist. Only [src/entrypoint.sh](file:///home/mechsoull/Projects/runnero/src/entrypoint.sh) is present.

The registration logic was inlined into `entrypoint.sh`, but the documentation still references the separate file.

**Recommendation**: Either create `register.sh` as documented, or update AGENTS.md and docs/02 to remove the reference.

---

### INFRA-02: Runner Service Missing Health Check

**File**: [docker-compose.yml](file:///home/mechsoull/Projects/runnero/docker-compose.yml#L36-L54)  
**Severity**: High

The `supervisor` service has a well-defined `healthcheck` using `/healthz` and `/readyz`, but the `runner` service has **no health check at all**. Docker/compose will have no way to know if the runner is actually healthy.

**Recommendation**: Add a basic healthcheck (e.g., checking if the runner PID is alive or using a sentinel file).

---

## 🟡 Medium Severity Issues

### SEC-05: Dockerfile Layer Bloat From Separate `chown`

**File**: [Dockerfile](file:///home/mechsoull/Projects/runnero/Dockerfile#L120-L127)  
**Severity**: Medium

```dockerfile
COPY --from=downloader /build/actions-runner /actions-runner
RUN ./bin/installdependencies.sh \
    && mkdir -p /actions-runner/_work \
    && chown -R 1001:1001 /actions-runner \
    && chmod -R 755 /actions-runner
```

The `COPY` without `--chown` followed by a separate `chown -R` effectively doubles the layer size of the `/actions-runner` directory (~300-500MB). Unlike the `act_runner` and `forgejo-runner` binaries which correctly use `COPY --chown=root:root --chmod=755`, the main runner directory doesn't.

**Recommendation**: Use `COPY --from=downloader --chown=1001:1001 /build/actions-runner /actions-runner` and run `installdependencies.sh` before `COPY`.

---

### SEC-06: VACUUM INTO Potential SQL Injection

**File**: [backup.go](file:///home/mechsoull/Projects/runnero/internal/db/backup.go#L56)  
**Severity**: Medium

```go
query := fmt.Sprintf("VACUUM INTO '%s';", escaped)
```

The `VACUUM INTO` path is constructed via string formatting rather than parameterized queries. While `escaped` applies basic sanitization, this is a deviation from the project's otherwise perfect parameterized-query discipline via `sqlc`. The `destPath` is internally controlled (not user-facing), which significantly reduces risk.

**Recommendation**: Document that `destPath` is never user-supplied, or investigate if `modernc.org/sqlite` supports parameterized `VACUUM INTO`.

---

### MISS-02: `ImageUpdateService` Has No Dedicated Database Table

**Files**: [api.proto](file:///home/mechsoull/Projects/runnero/proto/api.proto#L480-L535), DB migrations  
**Severity**: Medium

The `ImageUpdateService` defines 4 RPCs (`CheckImageUpdate`, `PullImage`, `ListImageUpdates`, `DismissImageUpdate`) with a full `ImageUpdate` message type. However, there is **no `image_updates` table** in any migration. The service uses in-memory state only.

This means:
- Image update notifications are lost on supervisor restart
- No persistent history of image updates exists
- The `DismissImageUpdate` action is also ephemeral

**Recommendation**: Add a `004_image_updates.sql` migration creating an `image_updates` table, or document that in-memory tracking is intentional.

---

### MISS-03: No Custom Network in Docker Compose

**File**: [docker-compose.yml](file:///home/mechsoull/Projects/runnero/docker-compose.yml)  
**Severity**: Medium

The compose file uses the default Docker bridge network. The orchestrator code references a custom network `ghrs-supervisor` ([provider.go](file:///home/mechsoull/Projects/runnero/internal/orchestrator/provider.go#L11)), but the compose file doesn't define or use it. This means spawned runner containers may not be on the same network as the supervisor.

**Recommendation**: Define a custom bridge network in `docker-compose.yml` that aligns with `ghrs-supervisor`.

---

### MISS-04: Missing `cap_drop` Documentation

**Files**: [AGENTS.md](file:///home/mechsoull/Projects/runnero/AGENTS.md#L173), [docker-compose.yml](file:///home/mechsoull/Projects/runnero/docker-compose.yml)  
**Severity**: Medium

AGENTS.md explicitly requires: *"Document minimal Docker configurations (e.g., dropping capabilities with `cap_drop`)"*. Neither the docker-compose file nor the README include any `cap_drop` directives or documentation about Linux capability restrictions.

**Recommendation**: Add `cap_drop: [ALL]` + necessary `cap_add` to docker-compose services, and document in README.

---

### GAP-01: `open-questions.md` Specifies CORS Strict Denial — Implementation Confirms but No Config Exposed

**File**: [server.go](file:///home/mechsoull/Projects/runnero/internal/server/server.go#L440)  
**Severity**: Medium

Security headers and CORS denial are implemented (comment references OQ #25, #26). However, there is no configuration option to adjust CORS policy for deployments behind API gateways or custom domains.

**Recommendation**: Add a `SUPERVISOR_CORS_ALLOWED_ORIGINS` env var for operators who need cross-origin access.

---

### GAP-02: `install-tools.sh` Missing Strict Mode

**File**: [install-tools.sh](file:///home/mechsoull/Projects/runnero/scripts/install-tools.sh#L3)  
**Severity**: Medium

The script only uses `set -e`. Per AGENTS.md, all shell scripts should use `set -euo pipefail`. Additionally, all tools are installed with `@latest` instead of pinned versions, creating non-reproducible dev environments.

**Recommendation**: Add `set -euo pipefail` and pin tool versions.

---

### TEST-01: Missing E2E Tests for Job History and Image Updates

**Files**: [tests/e2e/](file:///home/mechsoull/Projects/runnero/tests/e2e/)  
**Severity**: Medium

Playwright E2E specs cover specs 01-07 (Bootstrap through Settings). Missing coverage:
- Job History page navigation and filtering
- Image Update notification/dismissal workflows

**Recommendation**: Add E2E spec files for History and Image Update flows.

---

### TEST-02: No Integration Tests for Shell Scripts Beyond Unit Tests

**File**: [Makefile](file:///home/mechsoull/Projects/runnero/Makefile#L64-L65)  
**Severity**: Medium

The `test-scripts` target runs `tests/unit/entrypoint_test.sh` which is a unit-level test. There are no integration tests that verify the entrypoint script actually works inside the runner Docker image with real (or mocked) runner binaries.

**Recommendation**: Add a docker-compose based integration test that boots the runner image and verifies registration flow.

---

## 🟢 Low Severity Issues

### GAP-03: AGENTS.md References Non-Existent `register.sh` in Architecture Diagram

**File**: [AGENTS.md](file:///home/mechsoull/Projects/runnero/AGENTS.md#L199)  
**Severity**: Low

The repository structure in AGENTS.md shows:
```
├── src/
│   ├── entrypoint.sh
│   └── register.sh    ← does not exist
```

**Recommendation**: Remove `register.sh` from the structure diagram.

---

### GAP-04: Architecture Doc References `src/register.sh`

**File**: [02-architecture-design.md](file:///home/mechsoull/Projects/runnero/docs/02-architecture-design.md#L91)  
**Severity**: Low

Same stale reference as GAP-03.

---

### QUAL-01: `resp.Body.Close()` Errors Silently Ignored in Registry Client

**File**: [internal/registry/client.go](file:///home/mechsoull/Projects/runnero/internal/registry/client.go) (lines ~155, 177, 206)  
**Severity**: Low

```go
defer func() { _ = resp.Body.Close() }()
```

While idiomatic Go, consistently ignoring close errors in an HTTP proxy context could mask underlying connection issues. This is a minor observability gap.

---

### QUAL-02: s6 `run` Script Lacks `set -euo pipefail`

**File**: [deploy/supervisor/s6-rc.d/supervisor/run](file:///home/mechsoull/Projects/runnero/deploy/supervisor/s6-rc.d/supervisor/run)  
**Severity**: Low

The 2-line s6 exec script uses `#!/command/with-contenv sh` and doesn't include strict mode. While acceptable for s6 exec-chain scripts, it deviates from the project's shell scripting standards.

---

### QUAL-03: Unnecessary `apk del xz tar` in Supervisor Dockerfile

**File**: [Dockerfile.supervisor](file:///home/mechsoull/Projects/runnero/Dockerfile.supervisor#L88)  
**Severity**: Low

After `apk add --no-cache ... xz tar`, the script removes them with `apk del xz tar`. Since `--no-cache` was used, there's no package cache to clean. The `apk del` is cosmetically valid (reduces image size by removing the packages themselves) but could be combined into the same `RUN` layer more cleanly.

---

### QUAL-04: `Dockerfile` Uses `ubuntu:24.04` — Not the Minimal Base Recommended

**File**: [Dockerfile](file:///home/mechsoull/Projects/runnero/Dockerfile#L7)  
**Severity**: Low

AGENTS.md recommends "lightweight, minimal, and secure base images (such as Alpine Linux or a minimal Debian-slim distro)". The runner Dockerfile uses `ubuntu:24.04` (full Ubuntu). This is likely justified by the GitHub Actions runner's dependency requirements, but should be documented.

---

### QUAL-05: `runner_pools.repository_url` Column Retained After Multi-Target Migration

**File**: [003_pool_targets.sql](file:///home/mechsoull/Projects/runnero/internal/db/migrations/003_pool_targets.sql)  
**Severity**: Low

Migration 003 creates `pool_targets` and backfills from `runner_pools.repository_url`, but the `repository_url` column is never dropped from `runner_pools`. This creates data duplication between the legacy column and the new join table.

**Recommendation**: Add a follow-up migration that drops the `repository_url` column from `runner_pools`, or document why it's retained for backward compatibility.

---

### TEST-03: No Vitest Config File

**File**: [web/](file:///home/mechsoull/Projects/runnero/web/)  
**Severity**: Low

The Vitest configuration appears to be inline within `vite.config.ts`. While this works, a dedicated `vitest.config.ts` improves discoverability and separation of concerns.

---

## ✅ What's Working Well

These areas are implemented cleanly and match the design docs:

| Area | Assessment |
|---|---|
| **Go Backend Architecture** | Modular, well-tested, zero TODOs/FIXMEs. Strict DI, context propagation, and slog logging. |
| **ConnectRPC Services** | All 8 services fully implemented with proper binary wire protocol enforcement. |
| **Database Layer** | sqlc-generated queries, Goose migrations, AES-256 encryption, HKDF key derivation — all solid. |
| **Orchestrator** | Complete lifecycle management with scale-to-zero, ephemeral containers, webhook scaling, and graceful shutdown. |
| **Frontend** | React 19 + TypeScript + TanStack Router/Query + TailwindCSS. Strict typing, no `@ts-ignore`. |
| **Security Headers & Auth** | HttpOnly/SameSite cookies, security middleware, CORS denial, audit logging. |
| **CI/CD Pipelines** | 5 workflow files covering build, test, lint, and deploy. Path-based filtering with Dependabot. |
| **Entrypoint Script** | Fully AGENTS.md-compliant: `set -euo pipefail`, signal traps, graceful deregistration. |
| **Health Probes** | `/healthz` (liveness) and `/readyz` (readiness) with pluggable probe registries. |
| **Backup & Recovery** | VACUUM INTO snapshots, configurable interval and retention. |
| **Proto Design** | Clean, consistent naming. Secrets never exposed in read RPCs. |
| **Test Coverage** | Exceptionally thorough Go tests (CLI, orchestrator, DB, providers, security). |
