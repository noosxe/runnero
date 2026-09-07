# Open Questions & Clarifications

Before proceeding with the implementation of the AIO Supervisor, the following architectural and product questions need clarification from the product/engineering team:

## 1. Gitea & Forgejo Webhooks & Scale-to-Zero
- Phase 3 proposes Webhook integrations (`workflow_job.queued`) for real-time scaling and scale-to-zero for GitHub. Does Gitea or Forgejo provide an equivalent reliable webhook payload for job queuing that we can ingest? If not, do we rely strictly on the periodic auditor loop for Gitea/Forgejo pools?
> **✅ Resolved**: Gitea supports `workflow_job` webhooks (`queued`, `in_progress`, `completed` actions) — use event-driven scaling via an internal webhook endpoint. Forgejo does not support `workflow_job` webhooks — use API polling via the existing ~10s audit loop as a fallback. See updated sections in `02-architecture-design.md` and `03-lifecycle-and-orchestration.md`.

## 2. YAML vs. Database Reconciliation
- We mention both YAML import/export and the SQLite DB. We need to define the exact source of truth on boot. If a user mounts a static `config.yml` to the container, does it overwrite the SQLite state? Does it run in a "read-only GitOps mode" where the UI disables editing?
> **✅ Resolved**: Database is authoritative at runtime. YAML serves as an import/export format: imported as seed data on first boot (empty DB), ignored on subsequent boots. Export via UI/CLI for backup. Re-import via explicit `supervisor import --config config.yml` CLI command. See updated `02-architecture-design.md` section 4.

## 3. Environment Variable Specification
- We need to define the formal list of environment variables the Supervisor container itself requires or supports to boot (e.g., `PORT`, `DB_PATH`, `DB_ENCRYPTION_KEY`, `LOG_LEVEL`).
> **✅ Resolved**: The supervisor requires the following environment variables: `DB_ENCRYPTION_KEY` (**required**, supervisor refuses to start without it), `PORT` (default `8090`), `DB_PATH` (default `/data/supervisor.db`), `LOG_LEVEL` (default `info`), `DOCKER_HOST` (default `unix:///var/run/docker.sock`), `DATA_DIR` (default `/data`). JWT signing secret is derived from `DB_ENCRYPTION_KEY` via HKDF with a distinct context label — no separate `JWT_SECRET` env var.

## 4. Data Retention & Pruning Strategy
- We mention a 30-day retention window in the requirements, but we haven't defined the background cron worker or specific query responsible for purging old `job_history` rows and their associated log files to prevent disk exhaustion.
> **✅ Resolved**: A daily background cron job (midnight UTC) prunes `job_history` rows older than the configurable `job_retention_days` setting in `app_settings` (default: 30 days). Associated log files at `log_retention_path` are deleted alongside their rows. Pruning is skipped if the system is under heavy load (active runner count >80% of `total_allowed_runners`).

## 5. Error Handling & Rate Limiting
- How should the supervisor handle GitHub/Gitea/Forgejo API rate limits (e.g., backoff strategies) or Docker daemon unreachability without crashing the process or spamming logs?
> **✅ Resolved**: For GitHub, respect `Retry-After` / `X-RateLimit-Reset` headers and back off until the specified timestamp. For Gitea/Forgejo, use exponential backoff with jitter. Maximum backoff cap is 15 minutes. When the Docker daemon is unreachable, the supervisor enters a degraded state — pauses container spawning, logs errors, and retries periodically without crashing. Persistent errors (rate-limited >10 min, Docker down >5 min) surface as alert notifications in the Web UI dashboard.

---

## Authentication & Session Management

## 6. Session Token Mechanism
- `AuthService.Login` returns a `token` and `GetSession` validates it, but the docs never specify: JWT vs opaque session tokens? Token expiry duration and refresh strategy? Cookie-based vs `Authorization` header transport? How does ConnectRPC binary mode carry authentication context per-request (interceptors, metadata)?
> **✅ Resolved**: JWT tokens transported via `HttpOnly` secure cookies with `SameSite=Strict`. 24h expiry, configurable. Sessions tracked in the `sessions` database table for audit and forced revocation. ConnectRPC interceptors read the cookie automatically. See updated `08-rpc-protocols.md` and `07-database-schema.md`.

## 7. Multi-Admin & RBAC Model
- The `admin_users` table supports multiple rows and `GetSessionResponse` includes `is_admin`, implying multiple roles exist. Do we support multiple admin accounts? Are there non-admin viewer roles? If yes, what permissions does each role have? If not, should we simplify the schema and proto to remove `is_admin`?
> **✅ Resolved**: For MVP, all users in `admin_users` are admins by definition — the table name implies it. The `is_admin` proto field in `GetSessionResponse` is kept for future RBAC extensibility but always returns `true` at the service layer. No schema change needed; the value is derived, not stored. Full RBAC design deferred to post-MVP.

## 8. Password Reset / Recovery Flow
- There's no external auth (no email, no OAuth for the admin UI). If the local administrator forgets their password, what's the recovery path? Options: CLI reset command (`supervisor reset-password`), direct DB manipulation, or environment-variable-based recovery key?
> **✅ Resolved**: Password recovery via interactive CLI command `supervisor reset-password`. The admin `docker exec`s into the supervisor container and runs the command, which prompts for username and then new password (with confirmation). No env var recovery key or direct DB manipulation.

---

## Database & Schema

## 9. Missing `scope` Column on `runner_pools`
- The `GitProvider` interface defines `RegistrationScope` with values `repo`, `org`, and `global`, but the `runner_pools` table has no `scope` column. Org-level and global-level runner pools cannot be persisted or distinguished. Should we add a `scope TEXT NOT NULL CHECK(scope IN ('repo', 'org', 'global'))` column?
> **✅ Resolved**: Added `scope TEXT NOT NULL DEFAULT 'repo' CHECK(scope IN ('repo', 'org', 'global'))` column to `runner_pools` table. When scope is `org`, `repository_url` holds the org URL. When `global`, it holds the instance base URL. See updated `07-database-schema.md`.

## 10. Global Settings Keys & Defaults
- The `app_settings` table is a generic key/value store. The product requirements define `Total Allowed Runners` and `Total Idle Warm Pool` as global settings (Onboarding Step 3). We need to formalize: what are all expected keys, their value types, validation constraints, and default values? Should this be documented in the schema migration as seed data?
> **✅ Resolved**: Default global settings seeded on first boot via the initial migration: `total_allowed_runners` (20), `total_idle_warm_pool` (5), `shutdown_timeout_seconds` (300). See updated `07-database-schema.md`.

## 11. Session / Token Storage Table
- If admin login produces session tokens, there's no `sessions` table in the schema for tracking active sessions, token expiry, or revocation. Is session state managed in-memory only (lost on restart)? If so, is that acceptable for production use?
> **✅ Resolved**: Added `sessions` table with `id`, `user_id`, `token_hash` (SHA-256), `expires_at`, and `created_at` columns. See updated `07-database-schema.md`.

## 12. Audit Log Table
- For a security-sensitive system managing runner infrastructure and storing encrypted credentials, should we add an `audit_log` table to track admin actions (pool CRUD, credential changes, manual runner terminations, login attempts)? This has compliance and debugging implications.
> **✅ Resolved**: Added `audit_logs` table with `id`, `user_id`, `action`, `resource_type`, `resource_id`, `details` (JSON), and `created_at` columns. See updated `07-database-schema.md`.

---

## RPC Protocol Gaps

## 13. Auth Profile Management RPCs
- The `auth_profiles` table stores GitHub App, Gitea PAT, and Forgejo PAT credentials, and the Onboarding Step 2 requires creating/selecting them. However, `08-rpc-protocols.md` defines no CRUD service for auth profiles (e.g., `AuthProfileService` with `Create`, `List`, `Update`, `Delete`). These RPCs need to be defined.
> **✅ Resolved**: Added `AuthProfileService` with `ListAuthProfiles`, `CreateAuthProfile`, and `DeleteAuthProfile` RPCs. Sensitive fields (private keys, tokens) are write-only on create and exposed only as boolean indicators (`has_private_key`, `has_token`) on read. See updated `08-rpc-protocols.md`.

## 14. Streaming Log RPC
- Product requirements specify "Streaming Logs: Live logs of individual active runners." ConnectRPC supports `server-streaming` RPCs. We need to define an endpoint like `StreamRunnerLogs(StreamRunnerLogsRequest) returns (stream LogChunk)`. This also requires deciding the log capture mechanism (Docker log driver API, volume mounts, etc. — see Question 20).
> **✅ Resolved**: Live logs via server-streaming RPC `StreamRunnerLogs` using the Docker Container Logs API (`docker logs --follow`). `LogChunk` message includes structured metadata: `timestamp`, `stream` (`stdout`/`stderr`), and `content`. Historical logs for completed runners served via a separate unary RPC `GetRunnerLogs` (reads from stored log files). See also Question 20 for the persistence mechanism.

## 15. Onboarding State RPC
- The 5-step setup wizard needs backend state to determine whether setup is complete (show wizard vs. dashboard). Should there be a `GetOnboardingStatus` RPC? What state does it check — presence of an admin user, at least one auth profile, at least one pool?
> **✅ Resolved**: Added `OnboardingService` with `GetOnboardingStatus` RPC. Returns boolean flags: `admin_created`, `auth_profile_exists`, `pool_exists`, and `setup_complete` (all-true aggregate). See updated `08-rpc-protocols.md`.

## 16. Image Update Management RPCs
- Docs 01 and 03 describe periodic image update checks and admin notifications. We need RPCs to: list available image updates, trigger a manual pull, configure the automatic update schedule, and dismiss update notifications.
> **✅ Resolved**: Image updates are scoped per-pool (each pool has its own runner image). Update checks are manual on-demand only — admin clicks "Check for Updates" per pool. On update found, admin is notified and must manually trigger the pull. Full automation (auto-pull + rolling replacement) deferred to future. Dismiss is simple acknowledge (no "skip this version"). RPCs: `CheckImageUpdate`, `PullImage`, `ListImageUpdates`, `DismissImageUpdate` — all scoped to a pool. Per-pool scoping enables staged rollouts: validate on test pool first, then update production pools gradually.

## 17. Missing Resource Fields in Pool Proto
- The `Pool` proto message is missing `cpu_limit`, `memory_limit`, `max_runner_lifetime_seconds`, and `auth_profile_id` fields, even though these exist in the DB schema and YAML config. The frontend cannot configure resource limits without these fields.
> **✅ Resolved**: Added `auth_profile_id`, `scope`, `cpu_limit`, `memory_limit`, and `max_runner_lifetime_seconds` fields to the `Pool` proto message. Changed `labels` from `string` to `repeated string`. See updated `08-rpc-protocols.md`.

## 18. Renovate Trigger & Status RPCs
- Renovate has a DB table and cron scheduling, but no RPCs to manually trigger a Renovate run, view the last run status, or check upcoming scheduled runs. Should we add these to a `RenovateService` or extend `PoolService`?
> **✅ Resolved**: Dedicated `RenovateService` (not merged into `PoolService`). Supports both manual on-demand triggers and cron-scheduled runs. Status detail is moderate: last run time, status (success/failure/running), next scheduled run, and summary of changes found. RPCs: `TriggerRenovateRun`, `GetRenovateStatus`, `ListRenovateHistory`.

---

## Architecture & Operations

## 19. Supervisor Health Check Endpoints
- No document defines HTTP health/readiness endpoints (e.g., `GET /healthz`, `GET /readyz`) for the supervisor container. These are essential for Docker Compose `healthcheck` directives, load balancer integrations, and production monitoring. What should each check validate (DB connectivity, Docker socket reachability, active control loop)?
> **✅ Resolved**: Standard paths `GET /healthz` (liveness) and `GET /readyz` (readiness). Liveness validates: process alive + DB accessible. Readiness validates: DB connectivity + auditor/control loop running. Docker socket unreachable results in "degraded" status (still "ready" but flagged). Response format is JSON with per-check detail: `{ "status": "ready", "checks": { "db": "ok", "docker": "degraded", "auditor": "ok" } }`.

## 20. Log Capture Mechanism for Runner Containers
- The dashboard promises streaming logs and the schema has `log_retention_path`, but the docs never define *how* runner container stdout/stderr is captured by the supervisor. Options: Docker container logs API (`docker logs`), volume-mounted log files, Docker logging driver configuration? This decision affects the streaming RPC design, retention, and disk usage.
> **✅ Resolved**: On runner container exit, the supervisor reads the full log via the Docker Container Logs API and writes it to `DATA_DIR/logs/<runner-id>.log.jsonl.gz`. Format is structured JSONL (one JSON object per line with `timestamp`, `stream`, `content`). Files are gzip-compressed on write to save disk. Containers can be removed immediately after log capture. Pruned alongside `job_history` rows per the retention policy (see Question 4).

## 21. Backup & Disaster Recovery
- The SQLite database holds all critical state (encrypted credentials, pool configs, job history). No doc covers: recommended volume mount for the DB file, backup strategies (periodic SQLite `.backup` command?), or recovery procedures. What happens if the DB is corrupted?
> **✅ Resolved**: Docs recommend both bind mounts and named volumes for `/data`, noting bind mount for easier host-level backup access. Automated SQLite `.backup` snapshots run every 6 hours (configurable via `BACKUP_INTERVAL_HOURS` env var), stored at `DATA_DIR/backups/supervisor-<timestamp>.db`, retaining the last 7 (configurable via `BACKUP_RETENTION_COUNT`). All backup settings are env-var-only, not UI-configurable. A CLI command `supervisor backup` provides on-demand snapshots. On corruption: supervisor refuses to start with a clear, actionable error message directing the admin to restore from a backup.

## 22. Network Topology & DNS
- The architecture shows a compose stack but doesn't specify: Docker network mode (bridge/host/custom), DNS resolution between supervisor and ephemeral runners, or how the supervisor discovers the container engine endpoint if it's not `/var/run/docker.sock` (e.g., remote Docker host, TCP socket).
> **✅ Resolved**: The supervisor creates its own managed bridge network (e.g., `runnero-supervisor`) for runner communication, independent of whatever network the supervisor container itself runs on. Direct supervisor-to-runner communication (health probes, status checks) is planned beyond Docker API management. Multi-host Docker support: pools can be bound to different Docker hosts — local socket and remote TCP with TLS. This introduces a `docker_hosts` configuration concept with a `docker_host_id` reference on pool config. Runner container outbound network access is configurable per-pool (full access, restricted, or no internet) for security-sensitive workloads.

## 23. Container Naming Strategy
- The lifecycle doc defines container labels for tracking but doesn't specify the naming pattern for spawned containers (e.g., `runnero-<pool>-<short-hash>`). Clear naming is important for log readability, manual debugging (`docker ps`), and preventing name collisions.
> **✅ Resolved**: Container naming pattern: `runnero-<pool-slug>-<short-hash>` (e.g., `runnero-mypool-a3f2b1`). Pool slug is auto-generated from the user-facing pool label, truncated to fit Docker's 64-character container name limit (leaving room for the `runnero-` prefix and `-<6-char-hex>` suffix). Uniqueness via 6-character random hex suffix, no timestamp component. Rich metadata (pool ID, provider, scope) stored in Docker container labels.

## 24. Graceful Shutdown Timeout
- The shutdown sequence diagram shows the supervisor waiting for active runners to finish, but doesn't define: what's the maximum wait duration? What happens if a runner is stuck and never exits? Is there a configurable `shutdown_timeout_seconds` with a force-kill fallback?
> **✅ Resolved**: Added configurable `shutdown_timeout_seconds` (default: 300s) to `app_settings`. Shutdown behavior is signal-dependent: `SIGTERM` = graceful wait up to timeout then force-kill; `SIGINT` = immediate drain with Docker's default 10s stop grace period. See updated `03-lifecycle-and-orchestration.md`.

---

## Security

## 25. Web UI TLS / HTTPS Strategy
- The security doc covers credential encryption and container hardening but doesn't address transport security for the Web UI. Does the supervisor terminate TLS itself (self-signed cert generation)? Is a reverse proxy (nginx, Caddy, Traefik) expected in front? Should the docs recommend a deployment pattern?
> **✅ Resolved**: Supervisor listens on plain HTTP only. TLS termination is delegated to a reverse proxy. No self-signed certificate generation. Documentation will include example reverse proxy configurations for Caddy and Traefik (see [docs/10-reverse-proxy-tls.md](10-reverse-proxy-tls.md)). Native TLS termination may be considered in the future but is not prioritized.

## 26. CORS & HTTP Security Headers
- The SPA frontend is served by the Go backend. No document defines CORS policy, Content Security Policy (CSP), or other HTTP security headers (`X-Frame-Options`, `Strict-Transport-Security`). These are standard hardening measures for web applications.
> **✅ Resolved**: CORS is denied by default — no cross-origin requests allowed (same-origin only since the Go backend serves both the SPA and the API). Full standard security headers applied: `X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff`, `Content-Security-Policy` (restrict to self), `Referrer-Policy: strict-origin-when-cross-origin`. No `Strict-Transport-Security` header since TLS is the reverse proxy's responsibility.

## 27. Supervisor Docker Socket Permissions
- The security doc covers Docker socket isolation for *runner* containers (`allow_docker` flag), but doesn't detail the supervisor's own socket access model. What user/group does the supervisor process run as? Is the socket mount read-write (it must be to spawn containers)? What mitigations apply to the supervisor's elevated host access?
> **✅ Resolved**: For MVP, the supervisor process runs as root inside its container. Read-write Docker socket access is required and explicitly documented as an accepted risk. Security mitigations (socket proxy, capability restrictions, non-root execution) are deferred to post-MVP as a dedicated future work item. Documentation will clearly call out the risks of root-level socket access.

---

## Testing Strategy

## 28. Testing Framework & Strategy
- `AGENTS.md` references `tests/integration/` and `tests/unit/` directories, but no design doc covers: Go test framework conventions (`testing` + `testify`?), mocking strategy for Docker Engine SDK and GitHub/Gitea/Forgejo API clients, shell script testing (BATS?), frontend testing (Vitest + React Testing Library?), E2E/integration approach, or CI pipeline design for the supervisor.
> **✅ Resolved**: Go backend: standard `testing` package + `testify` for assertions. Mocking via interfaces with standard Go mocking tools (`mockery` or `gomock`). Frontend: Vitest + React Testing Library for component/unit tests. E2E testing skipped for MVP — rely on manual testing. CI pipeline: tests run in GitHub Actions on every PR. Shell script testing approach (BATS) to be decided at implementation time.

---

## Frontend / UI Design

## 29. UI/UX Wireframes & Component Spec
- Product requirements describe rich dashboards, a multi-step wizard, analytics graphs, and streaming logs, but there are no wireframes, component hierarchies, or page-level design specs. Should a design doc (e.g., `09-frontend-design.md`) be created before implementation to align on layout, navigation, and interaction patterns?
> **✅ Resolved**: A formal design doc (`09-frontend-design.md`) is required before frontend implementation begins — but creation is deferred to when frontend work starts. Design inspiration to be decided at that time. Navigation model: sidebar navigation. Color scheme: respect system preference (`prefers-color-scheme`), supporting both dark and light modes.

## 30. Real-time Dashboard Refresh Strategy
- The dashboard displays live pool states, active runner counts, CPU/memory stats, and streaming logs. No doc specifies the data refresh mechanism: polling with TanStack Query `refetchInterval`? WebSocket/SSE push? ConnectRPC server streaming? What's the acceptable latency for pool state updates?
> **✅ Resolved**: All real-time dashboard data (pool states, runner counts, resource usage) delivered via ConnectRPC server streaming — same pattern as `StreamRunnerLogs`. Near-realtime latency: server pushes updates as they occur. Consistent streaming pattern across the entire frontend for logs, dashboard state, and status updates.

## 32. Optional Onboarding Steps & Flexible First-Run Setup
- The initial 5-step onboarding flow strictly required configuring a Git Provider Auth Profile and an Initial Runner Pool before the dashboard could be unlocked. In many real-world scenarios, operators want to initialize administrative credentials first, explore the web interface, or configure Git providers and runner pools later. How should optional onboarding steps and the skip flow be handled across backend state, route guards, and UI?
> **✅ Resolved**: Step 1 (Admin Setup) is the only mandatory onboarding step required to enforce security and establish authenticated sessions. Steps 2 through 5 are optional:
> - Operators can skip individual steps or invoke a top-level `[ Skip to Dashboard ]` action at any point after Step 1.
> - An explicit `CompleteOnboarding` RPC marks `onboarding_completed = "true"` in `app_settings` and logs an `onboarding.complete` audit event.
> - `GetOnboardingStatus` evaluates `setup_complete` to `true` whenever `admin_created` is true AND either (`onboarding_completed` is true OR `pool_exists` is true).
> - Route guards allow immediate access to the authenticated app shell once `setup_complete` is true, with the Dashboard and Pools pages presenting clear empty-state guides when zero pools are provisioned.

---

## CI/CD & DevOps

## 33. Path-Based CI Workflow Filtering & Status Reporting
- All CI workflows in `.github/workflows/` (`go.yml`, `web.yml`, `lint.yml`, `build.yml`, `supervisor-build.yml`) currently execute either with top-level `paths:` filters or ad-hoc gate checks. Top-level `paths:` causes GitHub to skip entire workflows, leading to missing status check reports on PRs, while un-gated jobs waste expensive native runner minutes on docs-only or isolated changes. How should path-based filtering be standardized across all workflows?
> **✅ Resolved**: Adopt the standardized gatekeeper pattern using `dorny/paths-filter@v3` across all 5 workflows:
> - Every workflow implements an initial cheap `changes` gate job (~4s) running on `ubuntu-latest`.
> - Downstream test, lint, and build jobs specify `needs: changes` and evaluate `if: ${{ always() && (startsWith(github.ref, 'refs/tags/v') || github.event_name == 'workflow_dispatch' || needs.changes.outputs.<subsystem> == 'true') }}`.
> - Monitored path patterns are strictly mapped to subsystem boundaries: `cmd/**`, `internal/**`, `proto/**` for Go CI; `web/**`, `proto/**` for Web CI; `src/**`, `tests/**`, `Dockerfile*` for Lint CI.
> - Pull requests always trigger the gatekeeper jobs and cleanly report green without hanging status checks, while release tags (`v*`) and manual dispatches run downstream builds unconditionally. Comprehensive design documented in [docs/11-ci-cd-pipelines.md](11-ci-cd-pipelines.md).

## 34. Dependabot Ecosystem Configuration & Supply Chain Cool-Off Policy
- How should Dependabot be configured across the repository's diverse ecosystems (`github-actions`, Go modules, web frontend npm/pnpm, and Dockerfiles)? What update frequencies, pull request groupings, and supply chain safeguards should be enforced?
> **✅ Resolved**: Configure `.github/dependabot.yml` targeting four ecosystems on a synchronized weekly schedule (Mondays 04:00 UTC):
> 1. `github-actions` (dir: `/`): Weekly updates for GitHub Actions with `ci(github-actions)` commit prefix.
> 2. `gomod` (dir: `/`): Weekly updates for Go modules with `ci(gomod)` commit prefix and grouped minor/patch PRs.
> 3. `npm` (dir: `/web`): Weekly updates for frontend packages with `ci(npm)` commit prefix, grouped minor/patch PRs, and an enforced **1-day supply chain cool-off** (`cooldown.default-days: 1`) to protect against zero-day npm registry compromises.
> 4. `docker` (dir: `/`): Weekly updates for Docker base images with `ci(docker)` commit prefix.
> All Dependabot PRs automatically integrate with our `dorny/paths-filter@v3` gatekeeper architecture, running only relevant test and build suites. See detailed design in `docs/11-ci-cd-pipelines.md` Section 7.
