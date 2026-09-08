# Runnero — Multi-Provider Actions Runner & Supervisor

[![Go CI](https://github.com/noosxe/runnero/actions/workflows/go.yml/badge.svg)](https://github.com/noosxe/runnero/actions/workflows/go.yml)
[![Web CI](https://github.com/noosxe/runnero/actions/workflows/web.yml/badge.svg)](https://github.com/noosxe/runnero/actions/workflows/web.yml)
[![Lint](https://github.com/noosxe/runnero/actions/workflows/lint.yml/badge.svg)](https://github.com/noosxe/runnero/actions/workflows/lint.yml)
[![Runner Multi-Arch Build](https://github.com/noosxe/runnero/actions/workflows/build.yml/badge.svg)](https://github.com/noosxe/runnero/actions/workflows/build.yml)
[![Supervisor Multi-Arch Build](https://github.com/noosxe/runnero/actions/workflows/supervisor-build.yml/badge.svg)](https://github.com/noosxe/runnero/actions/workflows/supervisor-build.yml)

A lightweight, secure, and self-contained self-hosted runner and orchestrator stack for **GitHub Actions**, **Gitea Actions**, and **Forgejo Actions**. Packaged as production-ready, multi-architecture Docker containers designed to run natively on **ARM64** (Apple Silicon, AWS Graviton, Raspberry Pi) and **AMD64** hosts without emulation overhead.

---

## ✨ Features

- **All-in-One Multi-Provider Supervisor:** Database-driven daemon that automatically provisions, monitors, scales, and maintains dynamic pools of ephemeral runner containers across GitHub, Gitea, and Forgejo repositories.
- **Embedded Web UI & Flexible Onboarding Wizard:** Single-Page Application (React 19, TypeScript, TanStack Router & Query, TailwindCSS) embedded directly into the Go supervisor binary via `go:embed`. Features an administrator bootstrap setup, optional Git provider, safeguard, and pool configuration with instant "Skip to Dashboard" capability, zero-pool empty states with prerequisite guidance, dark/light theme switching, and live pool management.
- **Dynamic Ephemeral Scaling:** Automatically manages warm standby containers (`min_idle_runners`) ready for immediate job dispatch, auto-scales up to concurrency limits (`max_concurrency`), and aggressively prunes completed or failed containers within seconds.
- **Real-Time Streaming Logs & Interactive Terminal:** Unbuffered ConnectRPC server-sent streaming (`StreamRunnerLogs`, `StreamSystemMetrics`) pushing real-time container output directly to an embedded xterm.js terminal emulator with auto-scroll and quick-copy.
- **Dependency Automation via Renovate:** Built-in scheduled Renovate task runner for autonomous dependency updates, configured via cron expressions with isolated ephemeral container execution.
- **Single Master Key, Derived Secrets:** The single required `SUPERVISOR_DB_ENCRYPTION_KEY` expands via HKDF-SHA256 into two distinct, deterministic secrets — an AES-256 database encryption key for credentials at rest and a HMAC secret for JWT session tokens.
- **Embedded SQLite & Auto-Migrations:** Pure-Go SQLite persistence via `modernc.org/sqlite` (strictly CGO-free) with automated Goose migrations on boot, rolling snapshot backups (`SUPERVISOR_BACKUP_INTERVAL_HOURS`), and strict corruption detection.
- **Unified Multi-Provider Runner Image (`runnero`):** Multi-stage container image bundling GitHub Actions runner, Gitea `act_runner`, and Forgejo `forgejo-runner` with automatic provider detection, non-root user execution (`UID 1001`), and active signal traps for clean deregistration.
- **Automated Health Probes:** Serves `GET /healthz` (liveness: process and SQLite accessible) and `GET /readyz` (readiness: database, audit loop, and Docker daemon reachability; reports `degraded` during Docker outages while remaining responsive).
- **Reverse-Proxy TLS & Hardened Cookies:** Designed for TLS termination via external reverse proxies (Caddy, Traefik, Nginx) with unbuffered HTTP/2 streaming support and configurable `SUPERVISOR_SECURE_COOKIE` enforcing `Secure; HttpOnly; SameSite=Strict` attributes.
- **Intelligent Gatekeeper CI/CD Filtering:** Unified path-based filtering architecture (`dorny/paths-filter@v3`) across all 5 GitHub Actions workflows (`go.yml`, `web.yml`, `lint.yml`, `build.yml`, `supervisor-build.yml`), diffing PR file changes in ~4s and skipping unimpacted heavy test, lint, and build matrices without dropping required branch status checks.
- **Automated Dependabot Ecosystem Maintenance:** Automated weekly dependency updates via GitHub Dependabot across `github-actions`, `gomod`, `npm` (`/web`), and `docker` ecosystems, featuring grouped minor/patch updates and an enforced 1-day supply chain cool-off period on frontend npm packages to defend against zero-day registry compromises.
- **Modular Moby Engine SDK (M16):** Decoupled container orchestration engine utilizing `github.com/moby/moby/client` and `github.com/moby/moby/api`, eliminating monolithic Docker daemon transitive dependencies, removing legacy `replace` directives, and resolving upstream daemon CVE alerts.
- **Playwright End-to-End (E2E) Testing Suite (M17):** Hermetic, containerized Playwright test harness testing all human-usable web supervisor flows (authentication, onboarding wizard, pools management, streaming logs, Renovate, and maintenance) with local mock Git provider and Docker engine servers. Manual/local trigger only via `make test-e2e`.
- **Multi-Target Runner Pools & Upstream Discovery Wizard (M18):** 4-step guided creation wizard with automatic repository and organization target discovery across GitHub (App & PAT), Gitea, and Forgejo profiles. Enables a single runner pool to dynamically serve multiple repositories or organizations under unified concurrency quotas and round-robin warm standby provisioning, with strict scope homogeneity enforcement.
- **GitHub App Installation & Access Scope Management Flow (M19):** Seamless deep-linking to GitHub App installation and configuration pages (`/installations/new`), guided setup prompts in the pool creation wizard and auth profiles, live repository and organization scope adjustments via "Manage Access in GitHub", installation status badges ("Installed on N accounts" vs "Not Installed"), and automatic discovery refetching on window focus when returning to the supervisor dashboard.
- **Runner Pool Operational State & Real-Time Diagnostics (M20):** First-class operational state machine (`Healthy`, `Provisioning`, `Degraded`, `Paused`) with live orchestrator intent, classified diagnostic error codes (`AUTH_DECRYPTION_FAILED`, `PROVIDER_AUTH_FAILED`, `TARGET_NOT_FOUND`, `REGISTRATION_TOKEN_FAILED`, `DOCKER_ENGINE_ERROR`, `IMAGE_PULL_FAILED`, `GLOBAL_QUOTA_SATURATED`), secret sanitization, and real-time streaming updates to the Web UI (`WatchRunners`). Features prominent status banners, contextual remediation prompts, and dashboard warnings for degraded pools.
- **Auth Profile Editing & Credential Rotation (M21):** Full edit workflow for existing Git auth profiles (`UpdateAuthProfile` ConnectRPC per `docs/17`): safe renaming (pool wiring is untouched), in-place secret rotation for PAT / GitHub App private keys / Gitea & Forgejo tokens, and auth-method migration with automatic purging of the old method's credential material. Blank secret fields mean "keep the existing AES-256-GCM encrypted secret" — the write-only secret model is preserved end-to-end, duplicate names surface as already-exists errors, every change is audit-logged with metadata only, and upstream credential validation runs only when a secret actually changes.
- **Runner Image Package Parity & Passwordless Sudo (M22):** Workflow compatibility with GitHub's `ubuntu-24.04` hosted runners: the unified runner image installs the apt package set from `actions/runner-images` `toolset-2404.json` (69 packages, including `xvfb` for GUI-style test suites, tracked in a reviewed manifest with 6 documented container-inappropriate drops, per `docs/18`), grants the `runner` user passwordless sudo (`NOPASSWD:ALL` via a `visudo`-validated sudoers drop-in) so workflows can `sudo apt-get install` ad hoc, and sets hosted-compatible toolchain env (`ImageOS`, `ImageVersion`, `RUNNER_TOOL_CACHE=/opt/hostedtoolcache`) so `actions/setup-*` behave identically — while multi-GB upstream toolchains/SDKs stay deliberately excluded.
- **Runner State Busy Sync (M23):** True busy/idle runner state, independent of webhook delivery: every audit cycle the supervisor polls the forge's registered-runner API (optional `RunnerLister` provider interface — GitHub's runners endpoint returns `busy`/`online` per runner) and converges each tracked runner's busy flag, with `workflow_job` webhooks kept as the sub-second fast path. Missed or unconfigured webhook events can no longer leave executing jobs shown as idle — and can no longer let scale-down or `max_concurrency` enforcement terminate a runner mid-job. Offline runners and names absent from the listing keep their last-known state; listing failures fail open (`docs/19`).
- **Ghost Runner Sweep (M24):** Self-healing runner registrations: ungraceful container deaths (OOM kills, `docker kill`, host reboots, supervisor downtime) bypass `--ephemeral` cleanup and the entrypoint's signal trap, leaving orphaned `Offline` entries on the forge's Runners page for weeks. Every audit cycle the supervisor reuses the M23 provider listing to spot idle, offline, locally-untracked `runnero-*` registrations and removes them via the provider's deregistration API after 3 consecutive offline cycles — busy (possibly mid-job) and tracked runners are exempt, foreign (non-`runnero-`) registrations such as manually created runners are never touched, deregistration failures fail open and retry, and a per-cycle cap bounds bursts. Zero additional API calls: the sweep shares the listing the busy-state sync already fetches (`docs/20`).
- **Job History Recording (M25):** Working dashboard job metrics in every deployment mode, including machines that never receive webhooks: the busy-state transitions the supervisor already observes each audit cycle now open and close `job_history` rows (idle→busy = job start, busy→idle or container death = job end, with clean exit codes closing as `completed` and ungraceful deaths as `interrupted`), so Jobs Executed, Avg Runtime, the latency trend, and the History page finally report real data. Job conclusions arrive two ways: webhook `workflow_job` events enrich rows with queue timing, forge-provided timestamps, and conclusions via merge-safe upserts keyed by the external job id, and — for webhookless deployments — an optional per-completion forge lookup (`SUPERVISOR_ENRICH_JOB_CONCLUSIONS`, on by default where the provider supports it) recovers the runner's latest concluded job. Where neither path is available, the success-rate and queue-wait cards degrade to an explicit "—" instead of a fabricated 100%/0. Crash recovery closes rows orphaned by supervisor restarts, a one-open-row-per-runner invariant prevents duplicates, and recording is fail-open by design (`docs/21`).
- **Comprehensive Automated Test Suites:** Extensive test coverage across Go unit, race detection (`go test -race`), and testify test suites (`mockery`-backed Docker and GitProvider clients), runner script test harnesses, and 20+ frontend Vitest test suites.

---

## 🚀 Quickstart: Supervisor Stack (Recommended)

Deploy the complete supervisor daemon and web control interface using Docker Compose:

### 1. Configure the Environment
Clone this repository (or download `docker-compose.yml` and `.env.example`):

```bash
git clone https://github.com/noosxe/runnero.git
cd runnero
cp .env.example .env
```

Generate a 256-bit encryption key (minimum 32 bytes) and set it in your `.env` file:
```bash
# Generate encryption key
openssl rand -base64 32
```
Add the output to `.env`:
```env
SUPERVISOR_DB_ENCRYPTION_KEY=your_generated_base64_key_here
SUPERVISOR_PORT=8090
```

### 2. Launch the Supervisor Stack
Start the containerized supervisor daemon:
```bash
docker compose up -d
```

Verify that the supervisor is healthy:
```bash
docker compose ps
curl -s http://localhost:8090/healthz
# Expected output: {"status":"healthy","checks":{"db":"ok"}}
```

### 3. Complete or Skip Onboarding
Navigate to `http://localhost:8090` in your browser. The system will automatically direct you to the onboarding wizard:
1. **Create Master Administrator:** Set administrative credentials for the dashboard (mandatory).
2. **Connect Git Provider (Optional):** Connect GitHub (GitHub App or PAT), Gitea (PAT), or Forgejo (PAT), or skip to configure later.
3. **Global Scaling Safeguards (Optional):** Establish total allowed runner limits, idle warm pool quotas, and history retention.
4. **Initial Runner Pool Setup (Optional):** Configure your repository URL, warm standby targets, concurrency limits, and optional Renovate automation.
5. **Review & Launch:** Review configured settings and launch into the dashboard.

> Operators can also click **Skip to Dashboard** at any step after creating the administrator account to finalize onboarding immediately and configure authentication profiles or runner pools later.

---

## 🔑 Git Provider Setup & App Creation

The supervisor daemon manages authentication profiles to dynamically fetch short-lived runner registration tokens from your upstream Git providers. Follow these verified instructions to configure credentials for **GitHub**, **Gitea**, and **Forgejo**.

### 1. GitHub Configuration

GitHub supports two authentication methods: **GitHub App** (recommended for production) and **Personal Access Token (PAT)**.

#### Option A: GitHub App (Recommended)

GitHub Apps provide fine-grained, least-privilege permissions, automatic credential rotation via RSA private keys, and eliminate dependency on personal user accounts.

##### Step 1: Create the GitHub App
1. Navigate to **Developer settings** in GitHub:
   - **For an Organization:** Go to `https://github.com/organizations/<org>/settings/apps` (Organization **Settings** → **Developer settings** → **GitHub Apps**).
   - **For a Personal Account:** Go to `https://github.com/settings/apps` (User **Settings** → **Developer settings** → **GitHub Apps**).
2. Click **New GitHub App**.
3. Fill in basic details:
   - **GitHub App name:** Enter a unique name (e.g., `my-org-runner-supervisor`).
   - **Homepage URL:** Your supervisor URL (e.g., `https://runner.example.com`) or repository URL.
4. Configure **Webhook** (optional, enables real-time autoscaling):
   - Check **Active**.
   - **Webhook URL:** `https://<supervisor-domain>/hooks/github`
   - **Webhook secret:** Enter a high-entropy secret string (configure the same secret in the supervisor).

##### Step 2: Configure App Permissions
Select the exact permissions required for runner orchestration:

| Category | Permission | Access Level | Purpose |
| :--- | :--- | :---: | :--- |
| **Repository permissions** | **Administration** | **Read and write** | Request runner registration tokens for repository pools (`POST /repos/{owner}/{repo}/actions/runners/registration-token`). |
| **Repository permissions** | **Metadata** | **Read-only** | Mandatory default read access to basic repository metadata. |
| **Repository permissions** | **Actions** | **Read-only** | Monitor workflow job runs and scale runners based on job queuing. |
| **Repository permissions** *(Optional)* | **Contents** | **Read and write** | Required only if using built-in Renovate automated dependency updates. |
| **Repository permissions** *(Optional)* | **Pull requests** | **Read and write** | Required only if using built-in Renovate automated dependency updates. |
| **Repository permissions** *(Optional)* | **Workflows** | **Read and write** | Required only if Renovate updates Actions workflow files. |
| **Organization permissions** | **Self-hosted runners** | **Read and write** | Required if registering organization-wide runner pools (`POST /orgs/{org}/actions/runners/registration-token`). |

##### Step 3: Subscribe to Events
Under **Subscribe to events**, check:
- **Workflow job** (notifies the supervisor immediately when jobs are queued, in progress, or completed).

Click **Create GitHub App**.

##### Step 4: Generate Private Key and Note App ID
1. On the app's **General** settings page, locate and copy the numeric **App ID** (e.g., `123456`).
2. Scroll down to the **Private keys** section and click **Generate a private key**.
3. An RSA private key file (`.pem`) will download to your computer. Keep this file secure.

##### Step 5: Install the App
1. In the app settings sidebar, click **Install App**.
2. Click **Install** next to the target organization or account.
3. Choose **All repositories** or select specific repositories that will use the runner pools.

##### Step 6: Add to `runnero`
1. In the `runnero` Web UI, navigate to **Git Auth Profiles** → click **+ Add Git Auth Profile**.
2. Select **GitHub App**.
3. Provide:
   - **Profile Name:** Descriptive label (e.g., `github-production`).
   - **GitHub App ID:** Numeric App ID copied from Step 4.
   - **Private Key (.pem):** Paste the full contents of the downloaded `.pem` file (including `-----BEGIN RSA PRIVATE KEY-----` and `-----END RSA PRIVATE KEY-----`).
4. Click **Save Profile**.

> [!TIP]
> **Guided Installation & Scope Management:** If you haven't installed the app yet or need to grant access to additional repositories, `runnero` automatically detects this and displays a direct **Install GitHub App on Your Account ↗** button. In the Pool Creation Wizard, you can click **Manage Access in GitHub ↗** to adjust repository permissions at any time — returning to `runnero` automatically refreshes the available target repositories on window focus.

---

#### Option B: Personal Access Token (PAT)

If you prefer using a Personal Access Token instead of a GitHub App:

- **Fine-grained PAT:**
  1. Navigate to **Settings** → **Developer settings** → **Personal access tokens** → **Fine-grained tokens** → **Generate new token**.
  2. Select the repository resource owner and target repositories.
  3. Under **Repository permissions**, set **Administration** to **Read and write**.
- **Classic PAT:**
  1. Navigate to **Settings** → **Developer settings** → **Personal access tokens** → **Tokens (classic)** → **Generate new token (classic)**.
  2. Select the `repo` scope (for repository-level runners) and `admin:org` scope (for organization-level runners).
- **Add to `runnero`:** In **Git Auth Profiles**, select **GitHub PAT**, enter a profile name, and paste the token.

---

### 2. Gitea Configuration

Gitea Actions uses `act_runner`. The supervisor requests short-lived runner registration tokens on-demand from the Gitea API using a Personal Access Token (PAT).

#### Step 1: Create a Personal Access Token in Gitea
1. Log in to your Gitea instance.
2. Click your user avatar in the top right → select **Settings** → navigate to the **Applications** tab.
3. Under **Manage Access Tokens**:
   - **Token Name:** Enter a descriptive identifier (e.g., `runnero-supervisor`).
   - **Select Scopes:**
     | Scope | Access Level | Purpose |
     | :--- | :---: | :--- |
     | `repo` (or `write:repository`) | Read / Write | Generate registration tokens for repository runner pools. |
     | `write:organization` / `admin:organization` | Read / Write | Generate registration tokens for organization runner pools. |
     | `admin` (or `admin:actions`) | Admin | Generate registration tokens for site-wide / instance-level runner pools. |
4. Click **Generate Token** and copy the resulting token string immediately (Gitea will not display it again).

#### Step 2: Configure Webhooks for Autoscaling (Optional)
To enable real-time, event-driven runner provisioning for Gitea Actions:
1. In your Gitea repository or organization, go to **Settings** → **Webhooks** → click **Add Webhook** → select **Gitea**.
2. Configure webhook parameters:
   - **Target URL:** `https://<supervisor-domain>/hooks/gitea`
   - **HTTP Method:** `POST`
   - **POST Content Type:** `application/json`
   - **Secret:** Enter the shared HMAC secret matching your supervisor configuration.
   - **Trigger On:** Select **Custom Events** → check **Actions** (or `Workflow Job`).
3. Click **Add Webhook**.

#### Step 3: Add to `runnero`
1. In the `runnero` Web UI, navigate to **Git Auth Profiles** → click **+ Add Git Auth Profile**.
2. Select **Gitea PAT**.
3. Enter a **Profile Name** (e.g., `gitea-main`) and paste the generated token into **Personal Access Token (PAT)**.
4. Click **Save Profile**.
5. When creating a runner pool, provide the target repository URL (e.g., `https://gitea.example.com/org/repo`).

---

### 3. Forgejo Configuration

Forgejo Actions uses `forgejo-runner`. The supervisor communicates with Forgejo's API to dynamically fetch registration tokens.

#### Step 1: Create a Personal Access Token in Forgejo
1. Log in to your Forgejo instance.
2. Click your user avatar in the top right → select **Settings** → navigate to the **Applications** tab.
3. Under **Manage Access Tokens**:
   - **Token Name:** Enter a descriptive identifier (e.g., `forgejo-supervisor`).
   - **Select Scopes:**
     | Scope | Access Level | Purpose |
     | :--- | :---: | :--- |
     | `repo` (or `write:repository`) | Read / Write | Request runner registration tokens for repository pools. |
     | `write:organization` / `admin:organization` | Read / Write | Request runner registration tokens for organization pools. |
     | `admin` | Admin | Request runner registration tokens for system-wide pools. |
4. Click **Generate Token** and copy the generated token string.

#### Step 2: Autoscaling Mechanism (API Polling)
> [!NOTE]
> **Forgejo Event Handling:** Forgejo does not currently support `workflow_job` webhooks. The supervisor automatically detects Forgejo targets and falls back to **API Polling mode** (`ScalingPolling`), querying `/api/v1/repos/{owner}/{repo}/actions/tasks` on the periodic ~10s audit loop to detect queued jobs. No manual webhook configuration is needed on Forgejo.

#### Step 3: Add to `runnero`
1. In the `runnero` Web UI, navigate to **Git Auth Profiles** → click **+ Add Git Auth Profile**.
2. Select **Forgejo PAT**.
3. Enter a **Profile Name** (e.g., `forgejo-corp`) and paste the generated token into **Personal Access Token (PAT)**.
4. Click **Save Profile**.
5. When creating a runner pool, provide the target repository URL (e.g., `https://forgejo.example.com/org/repo`).

---

## 📦 Standalone Runner Deployment (Optional)

If you only need a single static runner for a specific GitHub repository without the supervisor daemon or dynamic autoscaling:

```bash
# Set runner credentials in .env
GITHUB_REPOSITORY_URL=https://github.com/owner/repo
RUNNER_TOKEN=your_short_lived_registration_token

# Spin up standalone runner using the runner-standalone compose profile
docker compose --profile runner-standalone up -d runner
```

To monitor runner registration logs:
```bash
docker compose logs -f runner
```

---

## ⚙️ Configuration & Environment Variables

### Supervisor Daemon (`runnero-supervisor`)

The supervisor daemon layers configuration in increasing precedence: **built-in defaults → configuration file (`--config`) → environment variables → CLI flags**.

| Variable | Type | Required | Default | Description |
| :--- | :---: | :---: | :--- | :--- |
| `SUPERVISOR_DB_ENCRYPTION_KEY` | String | **Yes** | — | Master key (min 32 bytes) used for AES-256 database encryption and HKDF JWT secret derivation. |
| `SUPERVISOR_PORT` | Int | No | `8090` | HTTP port for the ConnectRPC API and embedded web control interface. |
| `SUPERVISOR_DATA_DIR` | String | No | `/data` | Data directory for SQLite database, snapshot backups, and gzipped execution logs. |
| `SUPERVISOR_DB_PATH` | String | No | `/data/supervisor.db` | Explicit file path for the SQLite database. |
| `SUPERVISOR_LOG_LEVEL` | String | No | `info` | Logging verbosity: `debug`, `info`, `warn`, `error`. Logs format as structured JSON. |
| `SUPERVISOR_DOCKER_HOST` | String | No | `unix:///var/run/docker.sock` | Docker daemon endpoint for orchestrating runner containers. |
| `SUPERVISOR_BACKUP_INTERVAL_HOURS` | Int | No | `6` | Frequency of automated rolling SQLite snapshot backups in hours. |
| `SUPERVISOR_BACKUP_RETENTION_COUNT` | Int | No | `7` | Number of automated SQLite backups retained before pruning. |
| `SUPERVISOR_CONFIG` | String | No | — | Path to an optional YAML or TOML configuration file. |
| `SUPERVISOR_SECURE_COOKIE` | Bool | No | `false` | Enables the `Secure` attribute on auth cookies. Set to `true` when behind HTTPS. |
| `SUPERVISOR_ENRICH_JOB_CONCLUSIONS` | Bool | No | `true` | On job completion, queries the forge once for the runner's latest concluded job to recover the conclusion and external job id on webhookless deployments (`docs/21` §5.3). Supported for GitHub repo/org pools; failures degrade the row to `completed`. |

### Standalone Runner Container (`runnero`)

| Variable | Type | Required | Default | Description |
| :--- | :---: | :---: | :--- | :--- |
| `GITHUB_REPOSITORY_URL` | String | **Yes** | — | Target repository URL (e.g., `https://github.com/owner/repo`). |
| `RUNNER_TOKEN` | String | **Yes** | — | Temporary runner registration token from repository settings (1 hour expiry). |
| `RUNNER_NAME` | String | No | *container-id* | Runner display name in the provider dashboard. |
| `RUNNER_LABELS` | String | No | `self-hosted,linux,arm64` | Comma-separated list of runner labels for workflow targeting. |
| `RUNNER_WORKDIR` | String | No | `_work` | Workspace directory path inside the runner container. |

---

## 🔒 Security Architecture & Hardening

### Supervisor Docker Socket Access (Accepted Risk Callout)

> [!CAUTION]
> **Docker Socket Privileges:**
> The `supervisor` container mounts the host Docker socket (`/var/run/docker.sock`) read-write. The supervisor daemon itself runs as a **non-root user** (`supervisor`, UID 10000) with Docker group membership (GID 999) for socket access. This is required so the daemon can communicate with the Docker Engine SDK to dynamically create, inspect, attach logs to, and destroy ephemeral runner containers on the host. While the non-root execution eliminates the container-escape-to-host-root vector, the Docker socket mount still grants significant host-level control.

**Operational Hardening Recommendations:**
- **Host Isolation:** Deploy the supervisor on a dedicated virtual machine or host instance isolated from shared production application workloads.
- **Network Boundaries:** Do not expose the supervisor port (`8090`) directly to the public internet without an authenticating reverse proxy and firewall rules.
- **Docker Socket Proxies:** In high-compliance environments, front the Docker socket with a capability-filtering socket proxy restricting container creation parameters.

### Credential & Token Segregation
- **Zero Master Token Leakage:** Master credentials (GitHub App private keys, provider PATs) are encrypted at rest with AES-256 and are **never** mounted, passed as environment variables, or serialized into spawned runner containers.
- **Ephemeral Job Tokens:** Spawner routines fetch short-lived, single-use runner registration tokens from Git provider APIs immediately before container creation.

### Non-Root Runner Execution
- Ephemeral runner containers run under a dedicated, low-privilege system user (`runner`, `UID 1001`, `GID 1001`). Workloads inside the runner cannot write to host filesystem paths outside their designated volume bounds.

### Reverse-Proxy & TLS Hardening
The supervisor daemon intentionally serves unencrypted HTTP on loopback/internal networks, delegating TLS termination to reverse proxies (such as Caddy, Traefik, or Nginx).

- For complete reverse proxy configurations, HTTP/2 setup, and unbuffered ConnectRPC streaming directives, consult the comprehensive guide: **[docs/10-reverse-proxy-tls.md](docs/10-reverse-proxy-tls.md)**.
- When running behind TLS, ensure `SUPERVISOR_SECURE_COOKIE=true` is set to protect session tokens against plaintext interception.

---

## 🛠️ CI/CD Pipelines

Automated GitHub Actions workflows ensure continuous verification and multi-architecture publishing:

- **Go CI (`go.yml`):** Runs `go build`, `go vet`, unit tests, and the Go data race detector (`go test -race`) on native AMD64 (`ubuntu-latest`) and ARM64 (`ubuntu-24.04-arm`) runners on every PR and push to `main`.
- **Web CI (`web.yml`):** Automated static analysis (`oxlint`), code formatting check (`oxfmt`), Vitest unit test suite execution, and production Vite compilation for frontend changes.
- **Lint CI (`lint.yml`):** Runs `shellcheck` across all runner lifecycle scripts and `hadolint` across both `Dockerfile` and `Dockerfile.supervisor`.
- **Runner Multi-Arch Release (`build.yml`):** Native AMD64 and ARM64 parallel matrix build creating and publishing multi-arch manifests for `ghcr.io/<owner>/runnero` upon git tag release (`v*`).
- **Supervisor Multi-Arch Release (`supervisor-build.yml`):** Native multi-stage AMD64 and ARM64 build compiling the supervisor daemon and embedded UI into `ghcr.io/<owner>/runnero-supervisor` upon git tag release (`v*`).

For comprehensive pipeline architecture, gatekeeper filtering rules, and cross-subsystem trigger mapping, consult **[docs/11-ci-cd-pipelines.md](docs/11-ci-cd-pipelines.md)**.

---

## 🗺️ Roadmap & Future Enhancements

- **Multi-Host Clustering:** Support for distributed Docker hosts over mutual-TLS (mTLS) TCP sockets to schedule runner pools across heterogeneous node clusters.
- **Rootless & Socket-Proxy Isolation:** Alternative supervisor orchestration backends utilizing rootless Podman / Docker or gVisor runtimes to eliminate root socket mounts.
- **Enterprise SSO / OIDC:** Federated single sign-on integration supporting OpenID Connect (OIDC), Okta, Keycloak, and GitHub OAuth for supervisor administrative access.
- **Runner Pool Editing:** Non-destructive editing of pool configuration (targets, quotas, image, labels, rename) with idle-runner recycling that never disrupts running jobs — design in `docs/22-pool-edit-workflow.md`. [Design Phase]

---

## 📄 License

Distributed under the Apache 2.0 License. See `LICENSE` for more information.
