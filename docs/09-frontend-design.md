# 09. Frontend Design Specification (Web UI)

This document establishes the formal frontend architecture, layout system, component hierarchy, interaction workflows, and ASCII wireframes for the **GitHub Actions Runner AIO Supervisor Web Control Interface** (docs/01 §2, docs/06 §2, OQ #29, #30).

---

## 1. Architectural Foundations

### 1.1 Tech Stack & Ecosystem
The Web Control Interface is an embedded Single Page Application (SPA) compiled into static assets and served by the Echo v5 Go backend via `go:embed` with fallback SPA routing.

| Layer | Technology | Rationale & Standards |
| :--- | :--- | :--- |
| **Framework** | **React 19** + **TypeScript** | Strict type safety aligned with generated Protobuf stubs (`web/src/gen/*`). |
| **Package Manager** | **pnpm** | Mandated across the repository for all Node/frontend dependency management. |
| **Build Tooling** | **Vite** | Fast HMR in development; optimized Rollup chunks for Alpine multi-stage Docker build. |
| **Linting & Formatting** | **oxlint** + **oxfmt** | Rust-based Oxc toolchain replacing ESLint/Prettier for sub-second static analysis and formatting. |
| **Testing** | **Vitest** + **Testing Library** | Fast in-memory unit tests with `jsdom` test runner. |
| **Routing** | **TanStack Router** (`@tanstack/react-router`) | Type-safe search params, nested layouts, route loaders, and redirect guards. |
| **State & API** | **TanStack Query** (`@tanstack/react-query`) + **Connect-Web** | Binary Protobuf transport client (`@connectrpc/connect-web`), zero JSON transport. |
| **Styling** | **TailwindCSS** | Utility-first CSS; new/migrated code uses the preset's semantic tokens only (`bg-primary`, `text-muted-foreground`, …) — no raw palette classes (`slate-*`, `emerald-*`) and no manual `dark:` overrides. |
| **UI Components** | **shadcn/ui** (Base UI base) | Vendored via the shadcn CLI only; preset `b7QqIqFpNQ` owns the visual language (docs/27). Generated files in `web/src/components/ui/` are read-only — customization via composition wrappers, CSS variables, or config. `web/src/index.css` and `src/components/ui/**` are excluded from oxlint/oxfmt (preset-owned, CLI-formatted). |
| **Icons** | **Lucide React** (`lucide-react`) | Clean, consistent, lightweight SVG iconography. |

### 1.2 Binary Transport & Error Handling
Per docs/06 §1 and RUN-44:
- All RPC communication uses the ConnectRPC **binary wire protocol** (`application/proto` over HTTP/1.1 chunked or HTTP/2).
- The Go backend enforces `DisabledJSONCodec` and rejects `application/json` with `415 Unsupported Media Type`.
- The frontend Connect client is configured exclusively with binary serialization.
- Server errors (`connect.Code`) map deterministically to UI feedback:
  - `CodeUnauthenticated`: Clears local session cache and triggers redirect to `/login`.
  - `CodePermissionDenied` / `CodeFailedPrecondition`: Surfaces inline banner or modal error (e.g., cannot delete profile referenced by pools).
  - `CodeInvalidArgument`: Inline form field validation errors.
  - `CodeNotFound`: Empty state or 404 page.
  - `CodeInternal` / `CodeUnavailable`: Toast notification with action retry.

### 1.3 Theming & Design Tokens
The interface supports both **Light** and **Dark** modes via an in-app toggle persisted to `localStorage` (`use-theme` toggles the `dark` class on `<html>`; wired to Tailwind through `@custom-variant dark`).

Colors are **preset-owned**: the `b7QqIqFpNQ` preset (style `maia`, neutral base, sky primary, amber chart ramp, Inter / Source Sans 3) emits OKLCH semantic tokens into `web/src/index.css` (`:root` / `.dark` blocks mapped through `@theme inline`). Code must reference semantic tokens — never hard-coded palette values:

To re-theme later: build a preset at [ui.shadcn.com/create](https://ui.shadcn.com/create), read it back with `pnpm dlx shadcn@latest preset decode <code>`, then apply only the theme tokens with `pnpm dlx shadcn@latest apply <code> --only theme` in `web/` (component files stay untouched). The current preset is the original `b7QqImqdoe` (blue) re-themed sky in 2026-09 — same fields otherwise; full history in docs/27 §4.1.

```text
Semantic tokens (preset-owned, both modes):
  background / foreground, card, popover, primary, secondary, muted,
  accent, destructive, border, input, ring, sidebar-*, chart-1..5

App-specific additions (re-add if `apply --preset` rewrites index.css):
  --success  oklch(0.5 0.17 149.2)    light (dark: 0.627) — status pills, healthy states
  --warning  oklch(0.52 0.179 58.318) light (dark: 0.666) — degraded states
  --notice   oklch(0.545 0.2 295)      interrupted jobs (violet; brighter in dark mode)

Light-mode success/warning are AA-darkened below the chart-ramp values (RUN-256): they
render as small badge text in two pairings — tinted (`bg-X/10` + `text-X`) and filled
(`text-X-foreground` white) — and must hold 4.5:1 in both. Dark mode keeps the ramp
values (both pairings already pass on dark surfaces).
```

**Job status colors** — one vocabulary (the `job_history.status` CHECK in
migration 004), one component (`components/common/job-status-badge.tsx`,
docs/21 §5 semantics), one color per family everywhere:

| status | family | token |
|---|---|---|
| success | green | `success` |
| failure, timeout | red | `destructive` |
| completed, cancelled | gray | `muted` |
| queued | amber | `warning` |
| running | blue | `primary` (spinner icon) |
| interrupted | violet | `notice` |

Unknown/forward-compat statuses render the raw value in neutral gray. Renovate
runs (running/success/failure) reuse the same badge.


### 1.3.1 Component System (shadcn/ui) — as built

The component language is **shadcn/ui** (Base UI primitives), installed only via `pnpm dlx shadcn@latest add <component>` in `web/` and vendored into `web/src/components/ui/` — those files are never edited by hand; customization happens through composition, CSS variables, and config. App code references the primitives from there (`Button`, `Card`, `Dialog`, `Table`, `Empty`, `Chart`, …) and styles exclusively with the semantic tokens above; raw palette classes (`bg-blue-600`) are legacy and are being tokenized surface-by-surface (tracked on Linear, see the docs/27 close-out sweep for the residual counts). Binding rules, the adoption map, and per-phase history live in `docs/27-shadcn-ui-migration.md` — the source of truth for the component system. The streaming log viewer core (`components/terminal/log-terminal.tsx`) is deliberately custom (streaming, autoscroll, search — no registry equivalent); only its toolbar chrome is on shadcn primitives.

---

## 2. TanStack Router Route Tree & Navigation Model

```mermaid
graph TD
    Root["__root.tsx (Session Context, Theme, Toast Provider)"]
    Root --> Guard{"GetOnboardingStatus()"}
    
    Guard -->|!setup_complete| Onboarding["/onboarding (5-Step Wizard)"]
    Guard -->|setup_complete & !authenticated| Login["/login (Admin Sign-In)"]
    Guard -->|setup_complete & authenticated| AppShell["_authenticated (App Shell Layout)"]
    
    AppShell --> Dashboard["/ (Dashboard & Analytics)"]
    AppShell --> Pools["/pools (Runner Pools List)"]
    AppShell --> PoolDetail["/pools/$poolId (Pool Details, Runners, Config)"]
    AppShell --> History["/history (Job Execution Log)"]
    AppShell --> Profiles["/profiles (Git Auth Profiles)"]
    AppShell --> Renovate["/renovate (Renovate Bot Control)"]
    AppShell --> Settings["/settings (Global Constraints, Backups, Audit)"]
```

### 2.1 Route Guard & Redirect Matrix

| Current State | Requested Route | Action |
| :--- | :--- | :--- |
| `setup_complete: false` | Any route (except `/onboarding`) | **Redirect to `/onboarding`** |
| `setup_complete: false` | `/onboarding` | Allow access (public RPC `GetOnboardingStatus`) |
| `setup_complete: true`, Unauthenticated | `/onboarding` | Redirect to `/login` |
| `setup_complete: true`, Unauthenticated | Any protected route (`/`, `/pools`, etc.) | **Redirect to `/login?redirect=...`** |
| `setup_complete: true`, Authenticated | `/login` or `/onboarding` | Redirect to `/` |
| `setup_complete: true`, Authenticated | Any protected route | Allow access |

**Guard RPC failures fail closed (RUN-243).** The matrix above is only evaluated on a *successful* `GetOnboardingStatus` response. When the RPC itself fails (network blip, unreachable supervisor), the guards propagate the error instead of synthesizing a fresh-install default — the router renders a dedicated guard-error screen ("Can't reach the supervisor", retry button) on `/login`, `/onboarding`, and all protected routes. A failed status check must never look like a fresh install, or a transient error would strand a fully onboarded operator on the onboarding wizard, one submit away from re-running SetupAdmin against an existing database.
### 2.2 URL-Driven Tab State (RUN-257)

Tabbed routes keep the active tab as a **path segment** (`/settings/users`, `/logs/removals`, `/account/security`) instead of component state — each tab is its own child route, so browser back/forward walks the tab history and deep links open the exact tab (RUN-283, following the /account pattern from docs/37). Data state rides search params (`/logs/supervisor?boot=…`, `/logs/runners?runner=…`). The pool detail page (RUN-258) still uses a `?tab=` search param; migrating it is tracked separately.

- `validateSearch` on the route types the param as an optional string; the page component owns clamping.
- Clamping goes through the shared `resolveRouteTab(raw, allowed, fallback)` helper (`web/src/lib/route-tab.ts`): a missing, unknown, or role-forbidden value renders the fallback tab — never a hidden surface, never a crash.
- Tab buttons `navigate()` instead of `setState`, so every switch is a history entry.
- `/settings` specifics (RUN-282/RUN-283): tabs are `instance` / `constraints` / `images` / `backups` / `users`; visible tabs are role-scoped (docs/35 §2.4 — instance for every role, the rest admin-only; personal surfaces moved to `/account`). `/settings` redirects to the role default — Global Constraints for admins, Instance for viewers — and a viewer deep link like `/settings/users` redirects to `/settings/instance` at the route level.
- `/pools/$poolId` specifics (RUN-258): tabs are `runners` (default) | `config` | `renovate`; unknown values clamp back to `runners`, and the param is optional so every existing deep link into pool detail keeps working.

---

## 3. Global App Shell Layout

The authenticated layout (`_authenticated.tsx`) consists of a fixed sidebar navigation, top header bar, and main scrollable content area.

```text
+-----------------------------------------------------------------------------------------------+
|  [LOGO] Runnero Supervisor      |  [Status: Healthy]  Runners: 3/5  |  [Theme]  [User: admin v] |
+---------------------------------+-------------------------------------------------------------+
|  NAVIGATION                     |  BREADCRUMB: Dashboard > Pools > pool-arm64-prod           |
|                                 +-------------------------------------------------------------+
|  [D] Dashboard                  |                                                             |
|  [P] Runner Pools (3)           |  MAIN CONTENT VIEW                                          |
|  [H] Job History                |  (Rendered via <Outlet />)                                  |
|  [K] Auth Profiles (2)          |                                                             |
|  [R] Renovate Bot               |                                                             |
|  [S] Settings & Backups         |                                                             |
|                                 |                                                             |
|  -----------------------------  |                                                             |
|  [Doc] Architecture Docs        |                                                             |
|  [Out] Logout                   |                                                             |
+---------------------------------+-------------------------------------------------------------+
```

### 3.1 Sidebar Navigation Spec
- **Collapsible**: Toggles between expanded (240px) and icon-only rail (64px) on desktop; full drawer on mobile.
- **Active State**: High-contrast indicator with tinted accent background (`bg-blue-500/10 text-blue-600 dark:text-blue-400 font-semibold`).
- **Badge Indicators**: Runner count on `Pools`, pending updates badge on `Settings`.
- **Identity menu (RUN-282)**: the footer avatar opens a dropdown whose identity row is a real menu item — avatar, username, and live role label — navigating to `/account/security` (the account page's only entry point); sign-out sits below it.
- **Version line (RUN-251)**: the footer shows the running product version under the identity menu — tiny mono `text-[10px] text-muted-foreground`, short form (`v0.3.0-379`; commit hash dropped), full git-describe string in a tooltip. Sourced from the authenticated `GetSession` payload; post-auth only, so the version never becomes a pre-auth fingerprint.

---

## 4. Comprehensive Page Specifications & Wireframes

### 4.1 Page 1: 5-Step Onboarding Wizard (`/onboarding`)

**Goal**: Seamless zero-config first boot initialization (OQ #15, OQ #32, docs/01 §2.1). **Step 1 (Admin Setup) is strictly mandatory** to secure the daemon; all subsequent steps (2–5) are **optional** and can be skipped individually or bypassed entirely via a top-level **"Skip to Dashboard"** shortcut.

```text
+---------------------------------------------------------------------------------------+
|                                    RUNNERO SUPERVISOR           [ Skip to Dashboard ] |
|                                Initial System Onboarding                               |
|                                                                                       |
|   (1) Admin Setup  -->  (2) Git Auth  -->  (3) Constraints  -->  (4) Initial Pool  -->  (5) Review
+---------------------------------------------------------------------------------------+
|                                                                                       |
|   Step 1 of 5: Create Master Administrator (Mandatory)                                 |
|   Set the primary administrative credentials for your supervisor instance.             |
|                                                                                       |
|   +-------------------------------------------------------------------------------+   |
|   | Username                                                                      |   |
|   | [ admin                                                                     ] |   |
|   +-------------------------------------------------------------------------------+   |
|   | Password (min 12 characters)                                                  |   |
|   | [ •••••••••••••••••••••                                                     ] |   |
|   +-------------------------------------------------------------------------------+   |
|   | Confirm Password                                                              |   |
|   | [ •••••••••••••••••••••                                                     ] |   |
|   +-------------------------------------------------------------------------------+   |
|                                                                                       |
|   [ Security Notice: Admin credentials are protected with bcrypt + session tokens. ]  |
|                                                                                       |
|                                                            [ Next: Git Provider -> ]  |
+---------------------------------------------------------------------------------------+
```

#### Step Details:
1. **Step 1: Admin Setup (Mandatory)**
   - Calls `AuthService.SetupAdmin(username, password)`.
   - On success, automatically establishes session cookie.
   - Unlocks the subsequent optional steps and exposes the top-right `[ Skip to Dashboard ]` action.
2. **Step 2: Git Provider Auth Profile (Optional)**
   - Options: `GitHub App (Recommended)`, `GitHub PAT`, `Gitea PAT`, `Forgejo PAT`.
   - Inputs: Name, App ID, Installation ID, Private Key PEM upload, or Personal Access Token.
   - Action: `[ Test Connection ]` button verifies upstream credentials via `ValidateCredentials`.
   - Skip Actions:
     - `[ Skip Step -> ]`: Advances to Step 3 without persisting an auth profile.
     - `[ Skip to Dashboard ]`: Invokes `OnboardingService.CompleteOnboarding` and navigates directly to `/`.
   - Calls `AuthProfileService.CreateAuthProfile` if submitted.
3. **Step 3: Global Scaling Constraints (Optional)**
   - Configures system-wide safeguards:
     - `total_allowed_runners`: Max concurrency across all pools (Default: `20`).
     - `total_idle_warm_pool`: Idle reserve ceiling (Default: `5`).
     - `shutdown_timeout_seconds`: Graceful termination deadline (Default: `300`).
     - `job_retention_days`: History pruning age (Default: `30`).
   - Defaults are pre-seeded in SQLite; operators can click `[ Use Defaults & Next -> ]` or `[ Skip Step -> ]` to advance to Step 4 without changing values.
   - Calls `OnboardingService.SetAppSetting` for customized constraints.
4. **Step 4: Initial Runner Pool Setup (Optional)**
   - Inputs: Pool Name, Repository/Org URL, Scope (`repo`, `org`), Labels (comma-separated), Runner Image.
   - Resource Quotas: CPU Limit (e.g. `2.0`), Memory Limit (e.g. `4GB`).
   - Concurrency: `min_idle_runners` (Default: `1`), `max_concurrency` (Default: `5`).
   - Provider Enforcement: If provider is Gitea or Forgejo, `Allow Docker (dind/host)` toggle is locked to **Enabled** (`true`) per docs/05 §4.
   - **Prerequisite Awareness**: If Step 2 was skipped, an informational banner indicates that a Git Provider Auth Profile must be connected before runner pools can be provisioned, providing a clear `[ Skip Pool Setup -> ]` action.
   - Skip Actions: `[ Skip Step -> ]` advances to Step 5; `[ Skip to Dashboard ]` completes onboarding immediately.
   - Calls `PoolService.CreatePool` if configured.
5. **Step 5: Review & Confirm Launch (Optional)**
   - Displays summary cards for each section:
     - Configured items display their chosen parameters.
     - Skipped items clearly indicate default state (e.g. *"Git Provider: Skipped — add anytime from Profiles"*, *"Initial Pool: None — add anytime from Pools page"*).
   - Actions:
     - `[ Complete Setup & Launch ]` (when pool is configured) or `[ Finish Setup & Go to Dashboard ]`:
       1. Invokes `OnboardingService.CompleteOnboarding` RPC to set `onboarding_completed: "true"` and record audit log.
       2. If a pool was defined, triggers reconciler loop for dynamic provisioning.
       3. Navigates to `/` (Dashboard).
   - Route guards on `_authenticated` layout honor `setup_complete: true`, directing the user into the main navigation shell. Zero-pool states in `/` and `/pools` display friendly empty-state cards guiding the user to connect a profile and create a pool.

---

### 4.2 Page 2: Authentication Screen (`/login`)

```text
+---------------------------------------------------------------------------------------+
|                                                                                       |
|                                    +-----------------------------+                    |
|                                    |      RUNNERO SUPERVISOR     |                    |
|                                    |      Sign in to continue    |                    |
|                                    +-----------------------------+                    |
|                                    | Username                    |                    |
|                                    | [ admin                   ] |                    |
|                                    |                             |                    |
|                                    | Password                    |                    |
|                                    | [ •••••••••••••••••••••   ] |                    |
|                                    |                             |                    |
|                                    | [ Sign In ]                 |                    |
|                                    +-----------------------------+                    |
|                                    |  SameSite=Strict • 24h JWT  |                    |
|                                    +-----------------------------+                    |
|                                                                                       |
+---------------------------------------------------------------------------------------+
```

Passkey entry point (RUN-248, docs/34 §8): when `GetOnboardingStatus.passkey_available`
is true (WebAuthn configured; the value is config state, so the button never
flickers with enrollment), a divider and a **"Sign in with passkey"** button
render below the form. Clicking it runs the discoverable ceremony — no
username, no password: the credential identifies the user — and on success
navigates exactly like the password submit (same redirect handling, session
cookie set by the same server path). The button is absent when WebAuthn is
not configured; an unknown-credential rejection surfaces as inline guidance
("No passkey on this device is registered with this supervisor."), any other
failure shows the server's message. The username/password form is an
independent complete path, not a degraded mode (docs/34 §3.3).

---

### 4.3 Page 3: Main Dashboard (`/` or `/dashboard`)

**Goal**: Real-time observability of runner utilization, health alerts, operational state, and quick actions.

```text
+-----------------------------------------------------------------------------------------------+
| Dashboard Overview                                            [ Refresh ] [ + Create Pool ]   |
+-----------------------------------------------------------------------------------------------+
|  KPI CARDS                                                                                    |
|  +--------------------+ +--------------------+ +--------------------+ +--------------------+  |
|  | ACTIVE RUNNERS     | | IDLE WARM POOL     | | 24H JOBS (TOTAL)   | | POOL HEALTH        |  |
|  |  3 / 20            | |  1 / 2 (Target)    | |  142 jobs          | |  1 Degraded        |  |
|  |  Capacity: 15%     | |  1 Launching...    | |  Avg Queue: 4.2s   | |  1 Healthy         |  |
|  +--------------------+ +--------------------+ +--------------------+ +--------------------+  |
+-----------------------------------------------------------------------------------------------+
|  SYSTEM HEALTH & OPERATIONAL ALERTS                                                           |
|  [ OK ] Docker Engine: Connected (unix:///var/run/docker.sock) • 4 active containers          |
|  [ !  ] Pool "pool-arm64-prod" Degraded: ERR_AUTH_FAILED (Auth profile decryption error)       |
|         Action required: Re-enter private key credentials in Git Auth Profile "github-app-prod"|
|  [ !  ] Runner Image Update Available: ghcr.io/noosxe/runnero:v1.2.0 (Pool: pool-linux-ci) |
+-----------------------------------------------------------------------------------------------+
|  ACTIVE RUNNER POOLS                                                        [ View All Pools ] |
|  +-----------------------------------------------------------------------------------------+  |
|  | POOL NAME          | PROVIDER  | HEALTH       | RUNNERS (ACT/IDL) | INTENT              | ACTIONS   |  |
|  +--------------------+-----------+--------------+-------------------+---------------------+-----------+  |
|  | pool-arm64-prod    | GitHub    | [Degraded !] | 0 active / 0 idle | Provisioning (fail) | [Diag][>] |  |
|  | pool-gitea-dind    | Gitea     | [Healthy OK] | 1 active / 1 idle | Idle target met     | [Logs][>] |  |
|  +-----------------------------------------------------------------------------------------+  |
+-----------------------------------------------------------------------------------------------+
|  RECENT JOB EXECUTIONS (24h)                                              [ View Full History]|
|  +-----------------------------------------------------------------------------------------+  |
|  | STATUS  | RUNNER NAME             | POOL             | DURATION | QUEUE TIME | COMPLETED    |  |
|  +---------+-------------------------+------------------+----------+------------+--------------+  |
|  | SUCCESS | runnero-arm64-prod-a8f12c  | pool-arm64-prod  | 2m 45s   | 3.1s       | 2 mins ago   |  |
|  | SUCCESS | runnero-gitea-dind-99c01b  | pool-gitea-dind  | 4m 12s   | 5.4s       | 14 mins ago  |  |
|  | FAILED  | runnero-arm64-prod-33e14a  | pool-arm64-prod  | 0m 18s   | 2.8s       | 1 hour ago   |  |
|  +-----------------------------------------------------------------------------------------+  |
+-----------------------------------------------------------------------------------------------+
```

**Live-stream cache contract (RUN-245)**: the app shell's `useWatchDashboard`
connection pushes server snapshots into the TanStack Query cache every tick.
The stream computes system stats at the backend's default **24h** timeframe
(`WatchDashboard` calls `GetSystemStats` with an empty request), so its
payloads write only the `[analytics, systemStats, 24]` cache entry. Other
timeframe variants — the 7-day chart's `[analytics, systemStats, 168]` — keep
their own `GetSystemStats(timeframe)` fetches and are never overwritten by
stream data, so toggling the chart's timeframe sticks.

---

### 4.4 Page 4: Runner Pools Management (`/pools`)

```text
+-----------------------------------------------------------------------------------------------+
| Runner Pools                                                        [ + Create New Pool ]     |
| Manage ephemeral runner pools, scaling targets, and provider bindings.                        |
+-----------------------------------------------------------------------------------------------+
| Filters: [ Search by name... ]  Provider: [ All v ]  Scope: [ All v ]  Health: [ All v ]      |
+-----------------------------------------------------------------------------------------------+
| +-------------------------------------------------------------------------------------------+ |
| | pool-arm64-prod  [GitHub] [Repo]  [DEGRADED !]                [ Edit ] [ Trigger ] [ ... ] | |
| | Target: https://github.com/noosxe/runnero • Auth Profile: github-app-prod               | |
| | Current Intent: Reconciling warm pool: launching 1 idle runner (target: 1, current: 0)     | |
| | Last Reconciled: 4s ago • Reconciliation Loop: Active (every 10s)                          | |
| | Active: 0  |  Idle: 0 (Target: 1)  |  Max Concurrency: 10  |  Quotas: 4 CPU / 8 GB        | |
| | Lifetime Limit: 7200s (2h)  |  Docker: Disabled (Rootless)                                 | |
| | [Progress Bar: ---------------------------------------------------- 0% Capacity]          | |
| |                                                                                           | |
| | ! RECONCILIATION ERROR (ERR_AUTH_FAILED):                                                 | |
| |   Failed to generate registration token from GitHub: private key decryption error.         | |
| |   [Fix Auth Profile]  [Retry Reconcile]  [View Full Diagnostics]                          | |
| +-------------------------------------------------------------------------------------------+ |
| +-------------------------------------------------------------------------------------------+ |
| | pool-forgejo-main  [Forgejo] [Org]  [HEALTHY OK]              [ Edit ] [ Trigger ] [ ... ] | |
| | Target: https://git.internal.net/devops • Auth Profile: forgejo-token                     | |
| | Current Intent: Warm pool satisfied (1/1 idle runners)                                     | |
| | Last Reconciled: 2s ago • Polling: Active (every 10s)                                     | |
| | Active: 0  |  Idle: 1 (Target: 1)  |  Max Concurrency: 4   |  Quotas: 2 CPU / 4 GB        | |
| | Lifetime Limit: 3600s (1h)  |  Docker: Enabled (Mandatory for Forgejo)                    | |
| | [Progress Bar: =======--------------------------------------------- 25% Capacity]         | |
| +-------------------------------------------------------------------------------------------+ |
+-----------------------------------------------------------------------------------------------+
```

---

### 4.5 Page 5: Pool Detail & Live Containers (`/pools/$poolId`)

```text
+-----------------------------------------------------------------------------------------------+
| < Back to Pools    pool-arm64-prod  [DEGRADED !]                      [ Edit Pool ] [ Reload ]|
| https://github.com/noosxe/runnero • Profile: github-app-prod                                |
+-----------------------------------------------------------------------------------------------+
| OPERATIONAL STATE & RECONCILIATION DIAGNOSTICS                                                |
| Status: DEGRADED • Last Reconciled: 4s ago • Next Loop: in 6s • Active: 0 • Idle: 0 / 1      |
| Intent: Attempting to spin up 1 idle container to satisfy warm pool target (1)               |
|                                                                                               |
| ! DIAGNOSTIC ALERT: [ERR_AUTH_FAILED]                                                        |
|   Occurred: 2026-09-07T14:32:10Z (4 seconds ago, persisting for 3 reconcile cycles)         |
|   Details:  Failed to fetch registration token from GitHub: private key decryption failed     |
|   Cause:    Master encryption key unable to decrypt GitHub App private key for profile        |
|             "github-app-prod". Token endpoint returned 500 internal error.                    |
|   Suggested Fix: Open Git Auth Profiles, edit "github-app-prod", and re-enter private key.    |
|   [ Edit Auth Profile ]   [ Retry Reconcile Now ]   [ View Supervisor Log ]                   |
+-----------------------------------------------------------------------------------------------+
| Tabs: [ Runners & Containers (0) ]  [ Diagnostics & Logs ]  [ Job History (89) ]  [ Config ]  |
+-----------------------------------------------------------------------------------------------+
| LIVE CONTAINER INSTANCES                                                                      |
| +-------------------------------------------------------------------------------------------+ |
| | CONTAINER ID    | RUNNER NAME            | STATUS | IP ADDRESS   | UPTIME   | ACTIONS     | |
| +-----------------+------------------------+--------+--------------+----------+-------------+ |
| | (No containers active or idle. See Diagnostics banner above for provisioning failure)    | |
| +-------------------------------------------------------------------------------------------+ |
+-----------------------------------------------------------------------------------------------+
```

---

### 4.6 Page 6: Job Execution History (`/history`)

```text
+-----------------------------------------------------------------------------------------------+
| Job Execution History                                                  [ Export Sanitized CSV]|
| Historical execution records, queue latencies, and compressed execution logs.                 |
+-----------------------------------------------------------------------------------------------+
| Filters: [ Search runner name... ]  Pool: [ All Pools v ]  Status: [ All v ]  Time: [ Last 7d v]|
+-----------------------------------------------------------------------------------------------+
| +-------------------------------------------------------------------------------------------+ |
| | ID  | STATUS  | RUNNER NAME            | POOL            | QUEUE WAIT | DURATION | ACTIONS    | |
| +-----+---------+------------------------+-----------------+------------+----------+------------+ |
| | 104 | SUCCESS | runnero-arm64-prod-a8f12c | pool-arm64-prod | 2.4s       | 3m 12s   | [View Logs]| |
| | 103 | SUCCESS | runnero-gitea-dind-88e21a | pool-gitea-dind | 4.1s       | 5m 01s   | [View Logs]| |
| | 102 | TIMEOUT | runnero-arm64-prod-77b01a | pool-arm64-prod | 1.8s       | 2h 00s   | [View Logs]| |
| | 101 | FAILED  | runnero-arm64-prod-44c99b | pool-arm64-prod | 3.0s       | 0m 22s   | [View Logs]| |
| +-------------------------------------------------------------------------------------------+ |
| Showing 1 - 25 of 1,482 jobs                                 < Previous  [ 1 ] 2  3  Next >   |
+-----------------------------------------------------------------------------------------------+
```

---

### 4.7 Page 7: Unified Terminal Log Viewer (`/history/$jobId` or Live Modal)

```text
+-----------------------------------------------------------------------------------------------+
| Terminal: runnero-arm64-prod-a8f12c (Live Stream)                 [ Pause ] [ Auto-scroll: ON ]  |
| Stream: stdout/stderr multiplexed • Connection: Active (sub-second follow)     [ Download Log]|
+-----------------------------------------------------------------------------------------------+
| 1 | 2026-09-04T00:50:01Z [stdout] √ Connected to GitHub Actions API                          |
| 2 | 2026-09-04T00:50:02Z [stdout] Current runner version: '2.322.0'                           |
| 3 | 2026-09-04T00:50:03Z [stdout] Listening for Jobs...                                       |
| 4 | 2026-09-04T00:52:14Z [stdout] Running job: Build & Test Matrix (amd64)                    |
| 5 | 2026-09-04T00:52:18Z [stderr] Warning: Node.js 16 actions deprecated                      |
| 6 | 2026-09-04T00:54:32Z [stdout] Job succeeded with exit code 0                              |
| 7 | 2026-09-04T00:54:33Z [stdout] Cleaning up and deregistering runner...                     |
+-----------------------------------------------------------------------------------------------+
| Terminal Controls: [ Filter: All / stdout / stderr ]  [ Clear ]       Lines: 7 (Auto-scrolled)|
+-----------------------------------------------------------------------------------------------+
```

#### Features:
- **Streaming Mode**: Consumes `LogService.StreamRunnerLogs(runner_id)`. Automatically parses 8-byte Docker headers (`stdout`/`stderr`). Reconnects on transient disconnects, tears down immediately on modal close.
- **Historical Mode**: Consumes `LogService.GetRunnerLogs(runner_id)`. Decompresses gzipped JSONL on backend and displays lines with copy/download options.

---

### 4.8 Page 8: Git Auth Profiles (`/profiles`)

```text
+-----------------------------------------------------------------------------------------------+
| Git Authentication Profiles                                            [ + New Auth Profile ] |
| Credentials used by the supervisor to dynamically request ephemeral registration tokens.      |
+-----------------------------------------------------------------------------------------------+
| +-------------------------------------------------------------------------------------------+ |
| | github-app-prod  [GitHub App]                          [ Edit Profile ]  [ Delete Profile ]  | |
| | Encrypted AES-256 (Write-Only) • Private Key: Configured • Installed on 2 accounts        | |
| |                                              [ Configure Access ]                         | |
| +-------------------------------------------------------------------------------------------+ |
| +-------------------------------------------------------------------------------------------+ |
| | gitea-pat-token  [Gitea PAT]                           [ Edit Profile ]  [ Delete Profile ]| |
| | Encrypted AES-256 (Write-Only) • Token: Configured                                        | |
| +-------------------------------------------------------------------------------------------+ |
+-----------------------------------------------------------------------------------------------+
```

---

### 4.9 Page 9: Renovate Bot Management (`/renovate`)

```text
+-----------------------------------------------------------------------------------------------+
| Managed Renovate Bot                                                    [ Trigger Run Now ]   |
| Ephemeral task container scheduling for automated dependency updates (docs/03 §6).            |
+-----------------------------------------------------------------------------------------------+
| +-------------------------------------------------------------------------------------------+ |
| | POOL NAME          | CRON SCHEDULE       | LAST RUN STATUS | NEXT RUN         | ACTIONS     | |
| +--------------------+---------------------+-----------------+------------------+-------------+ |
| | pool-arm64-prod    | 0 2 * * * (2am UTC) | SUCCESS (3 PRs) | in 4 hours       | [Run Now]   | |
| | pool-gitea-dind    | 0 4 * * 1 (Mon 4am) | NO_CHANGES      | in 3 days        | [Run Now]   | |
| +-------------------------------------------------------------------------------------------+ |
+-----------------------------------------------------------------------------------------------+
| RECENT RENOVATE TASK RUNS                                                                     |
| +-------------------------------------------------------------------------------------------+ |
| | RUN ID | POOL            | STATUS  | DURATION | BRANCHES CREATED | COMPLETED               | |
| +--------+-----------------+---------+----------+------------------+-------------------------+ |
| | ren-42 | pool-arm64-prod | SUCCESS | 1m 45s   | 3 updates        | Yesterday at 02:01 UTC  | |
| +-------------------------------------------------------------------------------------------+ |
+-----------------------------------------------------------------------------------------------+
```

---

### 4.10 Page 10: Settings, Backups & Administration (`/settings`)

```text
+-----------------------------------------------------------------------------------------------+
| Supervisor Settings & Administration                                                          |
+-----------------------------------------------------------------------------------------------+
| Tabs: [ Instance ]  [ Global Constraints ]  [ Runner Image Updates ]  [ Database & Retention ]  [ Users ] |
+-----------------------------------------------------------------------------------------------+
| TAB: Instance (all roles, RUN-251)                                                            |
| +-------------------------------------------------------------------------------------------+ |
| | Version: v0.3.0-379-g2eb9bc2   Host OS: linux   Host architecture: amd64                  | |
| | (mono values from the GetSession payload; read-only)                                      | |
| +-------------------------------------------------------------------------------------------+ |
|                                                                                               |
| TAB: Global Constraints                                                                       |
| +-------------------------------------------------------------------------------------------+ |
| | Global Runner Quota (total_allowed_runners): [ 20      ] runners                          | |
| | Warm Idle Pool Limit (total_idle_warm_pool):  [ 5       ] runners                          | |
| | Graceful Shutdown Timeout (seconds):         [ 300     ] seconds                          | |
| | History Retention Period (days):             [ 30      ] days                             | |
| |                                                                                           | |
| |                                                                [ Save Changes ]           | |
| +-------------------------------------------------------------------------------------------+ |
|                                                                                               |
| TAB: Database Snapshots & Backups (DATA_DIR/backups/)                   [ + Create Backup Now]|
| +-------------------------------------------------------------------------------------------+ |
| | FILENAME                          | CREATED AT          | SIZE     | ACTIONS              | |
| +-----------------------------------+---------------------+----------+----------------------+ |
| | supervisor-backup-20260904-000000 | 2026-09-04 00:00:00 | 1.4 MB   | [Download] [Restore] | |
| | supervisor-backup-20260903-180000 | 2026-09-03 18:00:00 | 1.3 MB   | [Download] [Restore] | |
| +-------------------------------------------------------------------------------------------+ |
+-----------------------------------------------------------------------------------------------+
```

Security tab (removed in RUN-282) — personal surfaces moved to `/account`
(see §4.11); viewers now land on Instance alone.

### 4.11 Page 11: Account (`/account/security`, `/account/sessions`)

RUN-282 (docs/37): the caller's personal surfaces, reached **only** from the
sidebar footer identity menu — the avatar row is a real menu item
(username + live role label) navigating to `/account/security`; sign-out
stays a separate item. Tabs are **path-based**: each tab is its own route,
so deep links, refreshes, and back/forward restore the tab without
`?tab=` params; `/account` redirects to `/account/security`. Both tabs are
available to every role.

- **Security tab** — Change Password card (docs/32) plus the Passkeys card
  (RUN-248, docs/34 §4.2). Passkey visibility is role-aware: with
  `passkey_available` true the card renders for every role (viewer
  enrollment is intentional — docs/35 §2.5); when WebAuthn is not
  configured the card renders **for admins only** as a shadcn `Empty` state
  naming `SUPERVISOR_WEBAUTHN_RP_ID` / `SUPERVISOR_WEBAUTHN_ORIGINS`, so
  the capability is discoverable instead of silently hidden.

- **List**: name, added, last used, and custody/signal chips — `Synced` vs
  `Device-bound` (docs/34 §3.2) and, when the server flagged the credential,
  a destructive `Cloned?` chip plus a red banner explaining that the flagged
  passkey can no longer sign in and should be removed.
- **Add passkey**: dialog collects the current password (the §4.2 re-check —
  a stolen-but-live session must not mint a complete login identity) and an
  optional label, then runs the browser creation ceremony; the submit button
  stays pending for the duration of the prompt. Server rejections (wrong
  password, replacement cap) surface inside the dialog.
- **Rename** / **Remove**: rename in place (1..64 chars); removal behind a
  confirm dialog that notes the password fallback is unaffected. No passkey
  action ever revokes sessions — that is deliberately the session list's
  lever (docs/34 §4.4).

Users tab — account management card (RUN-236, docs/35 §2.4), rendered only
for admins (a viewer's settings page is the Instance tab alone):

- **List**: username (with a `you` badge on the caller's row), role chip
  (`Admin` / `Viewer`), creation date, and per-row actions — role change,
  password reset, delete.
- **Add user**: dialog (username ≤64 chars, initial password with a
  client-side repeat check, role select); server rejections (duplicate
  username) surface inside the dialog.
- **Role change** and **delete** run behind confirm dialogs; the caller's
  own delete button is disabled (the server refuses self-delete
  regardless), and self-demote warns that the session downgrades on the
  next request. Last-admin refusals surface as toasts — the server, not
  the UI, is the enforcement point.
- **Reset password**: new password ×2; on success the toast reports how
  many of the target's sessions were revoked.

Role-aware rendering elsewhere (docs/35 §2.4): viewers get read-only
surfaces everywhere — pools lose their create/edit buttons, pool detail
hides edit/delete/terminate, auth profiles render an "Admin role required"
empty state, and the logs page drops the supervisor/removals tabs (their
`admin_users.role`-keyed data is admin-bucket server-side). `PermissionDenied`
anywhere in the app shows an "Admin role required" toast and keeps the
session — the opposite of `Unauthenticated`, which routes to login.

---

## 5. Dialogs & Modal Specifications

### 5.1 `PoolWizardModal` (shared create/edit)

The pool create and edit flows share a single **4-step guided wizard** (`web/src/components/pools/pool-wizard-modal.tsx`, `mode: "create" | "edit"`; docs/22 §7.2). Edit mode prefills every step from the pool being edited. with upstream target auto-discovery (docs/14 §4), eliminating copy-pasting URLs and enabling multi-target pool assignments:

- **Step 1: Pool Identity & Authentication**
  - **Pool Name**: Slug format (`^[a-z0-9-]+$`, max 40 characters) with real-time uniqueness validation.
  - **Git Auth Profile**: Dropdown selector displaying profile name, provider badge (`GitHub`, `Gitea`, `Forgejo`), and auth type (`GitHub App`, `PAT`). Provider is deduced automatically from the selected auth profile.
- **Step 2: Pool Scope & Discovered Target Selection**
  - **Pool Scope Dropdown**: `repo` (Repository-scoped) or `org` (Organization-scoped).
  - **Homogeneous Target Constraint**: Mixing `repo` and `org` in the same pool is strictly prohibited by UI and backend validation.
  - **Discovery Engine**: The UI invokes `PoolService.DiscoverTargets({ auth_profile_id, scope })` upon entering Step 2 or switching scope. Zero manual URL copy-pasting.
  - **Target Multi-Selection List**:
    - Instant live filter / search input (matches name or URL).
    - Quick actions: `[ Select All Filtered ]`, `[ Clear Selection ]`.
    - Checkbox cards displaying avatar, target name, private/public badge, description, and link.
    - Selected counter: e.g., `3 repositories selected`.
- **Step 3: Runner Specifications & Quotas**
  - **Concurrency Quotas**: `min_idle_runners` (min 0, default 1) and `max_concurrency` (min 1, default 5) shared dynamically across all targets in the pool.
  - **Runner Labels**: Comma-separated or chip tags automatically pre-populated with host arch suggestions (e.g., `self-hosted,linux,amd64` or `self-hosted,linux,arm64`).
  - **Runner Image**: Text input (default: `ghcr.io/noosxe/runnero:latest`).
  - **Docker Engine Privileges (`allow_docker`)**: Toggle checkbox. Automatically checked and disabled (locked true) if provider is Gitea or Forgejo.
  - **Resource Quotas**: CPU Limit (e.g., `2.0`), Memory Limit (e.g., `4GB`).
  - **Max Lifetime**: Seconds (default: `7200` / 2 hours).
  - **Renovate Bot (Optional)**: Enable toggle, cron schedule (`0 2 * * *`), and container image (`renovate/renovate:latest`).
- **Step 4: Review & Confirmation** *(create)* / **Review & Save** *(edit)*
  - Create: summary card displaying pool name, provider, scope, list of selected target URLs, runner image, quotas, and labels before final submission to `PoolService.CreatePool`.
  - Edit: **changed-fields diff** (old → strikethrough → new, restricted to fields that actually changed) plus impact banners computed from the field classes in docs/22 §5.2 — spawn-identity changes show the idle-runner recycle count, renames show the busy-runner guard warning. Submits the full Pool message (including `id`) to `PoolService.UpdatePool`; server rejections (e.g. `already_exists`, `failed_precondition`) render as banner text.

**Edit-mode deltas (docs/22 §7):**
- Entry points: **Edit** action on each pool card (`/pools`) and **Edit Configuration** in the pool detail Config tab.
- The auth profile selector is **locked to the pool's provider family** — provider is immutable after creation (docs/22 §5.3).
- Step 2 shows selected targets as removable chips so prefilled targets absent from discovery can still be deselected.
- `max_runner_lifetime_seconds` is preserved from the stored pool (not wizard-editable) instead of being reset to the create default.
- The Renovate tab on the pool detail page remains the shortcut for renovate-only edits (full-pool round-trip through `UpdatePool`).


### 5.2 `CreateAuthProfileModal`
- **Fields**:
  - Name: Identifier string.
  - Provider & Method Tabs:
    - **GitHub App**: App ID, Installation ID, Private Key PEM file dropzone or paste textarea.
    - **GitHub PAT**: Personal Access Token input.
    - **Gitea / Forgejo PAT**: Instance URL + Token input.
  - Actions:
    - `[ Test Connection ]`: Validates credentials with upstream provider without closing modal.
    - `[ Save Profile ]`: Submits to `AuthProfileService.CreateAuthProfile`.

### 5.3 `DeleteConfirmationModal`
- Reusable danger confirmation modal.
- Shows resource name, warns of impact (e.g., active containers will be gracefully drained).
- Input confirmation: Type name of resource to confirm if high-impact.

---

## 6. Interaction Workflows (Mermaid Diagrams)

### 6.1 Real-Time Streaming Log Follow Workflow
```mermaid
sequenceDiagram
    autonumber
    actor User as Admin UI
    participant Term as Terminal Component
    participant Client as ConnectRPC Client
    participant Server as Echo Server (LogService)
    participant Engine as Docker Engine
    
    User->>Term: Click "View Live Logs"
    Term->>Client: StreamRunnerLogs({ runner_id })
    Client->>Server: HTTP POST /supervisor.v1.LogService/StreamRunnerLogs
    Server->>Engine: ContainerLogs(Follow=true, Timestamps=true)
    
    loop Real-Time Chunk Push
        Engine-->>Server: 8-byte Header + Multiplexed Payload
        Server-->>Client: stream LogChunk { timestamp, stream, content }
        Client-->>Term: Append to Virtualized Buffer & Auto-Scroll
    end
    
    User->>Term: Close Modal / Navigate Away
    Term->>Client: AbortController.abort()
    Client->>Server: HTTP TCP Cancel / Reset Stream
    Server->>Engine: Close Log Stream Reader
    Note over Server,Engine: Clean stream teardown, zero goroutine leak
```

### 6.2 Pool Mutation & Hot-Reload Workflow (create/edit)
```mermaid
sequenceDiagram
    autonumber
    actor User as Admin UI
    participant UI as Pool Form
    participant Server as PoolService
    participant DB as SQLite DB
    participant Ctrl as PoolController
    
    User->>UI: Update min_idle (1 -> 3)
    UI->>Server: UpdatePool({ id, min_idle_runners: 3 })
    Server->>DB: UPDATE runner_pools SET min_idle_runners = 3
    Server->>DB: INSERT INTO audit_logs (pool_update)
    Server->>Ctrl: Reload(ctx)
    Note over Ctrl: Control loop immediately reconciles<br/>target idle deficit without restart
    Server-->>UI: UpdatePoolResponse
    UI->>User: Show Success Toast & Update Active Count
```

**Edit semantics (docs/22 §5.5):** before the DB write, the server validates the payload, rejects provider changes (`CodeInvalidArgument`), guards renames on zero busy runners via `PoolStats` (`CodeFailedPrecondition`), and — when a spawn-identity field changed or the pool is renamed — asks the controller to `RecycleIdleRunners` so respawns pick up the new configuration. Busy runners are never touched. Duplicate names surface as `CodeAlreadyExists`; `pool_targets` rows are rewritten only when the normalized target set changed; the audit entry records before/after values restricted to changed fields.

---

## 7. Component Hierarchy & Reusable Primitives

```text
web/src/
├── components/
│   ├── ui/                         # Atomic Design Primitives
│   │   ├── button.tsx              # Primary, secondary, danger, ghost, loading states
│   │   ├── input.tsx               # Text, number, password, search inputs
│   │   ├── select.tsx              # Styled dropdown selects
│   │   ├── checkbox.tsx            # Form checkboxes
│   │   ├── badge.tsx               # Status badges (success, error, warning, info)
│   │   ├── card.tsx                # Container cards with header, body, footer
│   │   ├── modal.tsx               # Accessible dialogs with focus traps
│   │   ├── table.tsx               # Data tables with sorting and pagination
│   │   ├── tabs.tsx                # Tabbed interfaces
│   │   ├── toast.tsx               # Floating feedback notifications
│   │   └── stat-card.tsx           # Dashboard KPI display cards
│   ├── layout/
│   │   ├── app-shell.tsx           # Global sidebar + header layout
│   │   ├── sidebar.tsx             # Collapsible navigation sidebar
│   │   ├── header.tsx              # Top bar with status pill & profile
│   │   └── page-header.tsx         # Page title, breadcrumbs, and actions
│   ├── terminal/
│   │   ├── terminal-viewer.tsx     # Virtualized monospace log viewer
│   │   └── terminal-controls.tsx   # Filter, auto-scroll, clear, download
│   ├── pools/
│   │   ├── pool-health-badge.tsx   # Status badge (Healthy, Provisioning, Degraded, Paused)
│   │   ├── pool-status-banner.tsx  # Operational intent & last reconciled time banner
│   │   └── pool-diagnostics-card.tsx # Detailed error diagnostic panel with remediation actions
│   └── forms/
│       ├── pool-form.tsx           # Reusable Create/Edit pool form
│       └── auth-profile-form.tsx   # Credentials input with test button
├── hooks/
│   ├── use-session.ts              # Session validation & logout handler
│   ├── use-theme.ts                # System preference listener & theme toggle
│   ├── use-log-stream.ts           # ConnectRPC streaming log consumer
│   └── use-pools.ts                # Pool queries, mutations, and cache invalidation
├── routes/
│   ├── __root.tsx                  # Root layout, QueryClient, ToastProvider
│   ├── login.tsx                   # Auth page
│   ├── onboarding.tsx              # 5-step wizard container
│   ├── _authenticated.tsx          # Authenticated App Shell layout
│   ├── _authenticated/
│   │   ├── index.tsx               # Dashboard view
│   │   ├── pools/
│   │   │   ├── index.tsx           # Pools list
│   │   │   └── $poolId.tsx         # Pool detail & runners
│   │   ├── history/
│   │   │   ├── index.tsx           # Job history list
│   │   │   └── $jobId.tsx          # Historical job & log viewer
│   │   ├── profiles.tsx            # Auth profiles management
│   │   ├── renovate.tsx            # Renovate bot management
│   │   └── settings.tsx            # Global settings & backups
└── main.tsx                        # Entrypoint, TanStack Router mount
```

---

## 8. Summary of Validation & Safety Guardrails

1. **Write-Only Credentials**: The UI never expects, requests, or stores raw private keys or tokens on read operations. Displays boolean badges (`has_private_key`, `has_token`).
2. **Provider Enforcement**: If Gitea or Forgejo is selected as the pool provider, the `allow_docker` checkbox is automatically checked and locked to `true` to ensure container workflows function.
3. **Referential Integrity Protection**: Pools referencing an auth profile warn the user, and profile deletion is blocked with an informative dialog if pools still reference it.
4. **Clean Stream Teardown**: Closing log viewer components triggers `AbortController.abort()`, releasing server streams and Docker follow readers immediately.
5. **Real-Time Operational State & Diagnostics**: The UI surfaces pool orchestrator intent, health state (`HEALTHY`, `PROVISIONING`, `DEGRADED`, `PAUSED`), and structured error codes (`ERR_AUTH_FAILED`, `ERR_DOCKER_DAEMON`, etc.) immediately, eliminating silent runner pool provisioning failures.


---

## 9. Form Validation Toolkit (protovalidate + TanStack Form, RUN-216 / docs/30)

The web holds **zero hand-written validation rules**. Rules live in
`proto/api.proto` as protovalidate annotations and are enforced server-side
(docs/08); the browser evaluates the *same* annotations on the *same* typed
request messages via `@bufbuild/protovalidate` as a fast, fail-first preview
(docs/30 §5.1–5.4).

### 9.1 Toolkit layout (`web/src/lib/forms/`)

```
lib/forms/
  protovalidate.ts    # one validator; validateMessage(schema, msg) → ViolationView[]
  violations.ts       # RULE_ID registry mirror + violationsFromConnectError(err)
  contexts.ts         # createFormHookContexts (field/form contexts)
  use-app-form.ts     # createFormHook → useAppForm (the ONLY form hook)
  fields/             # TextField, CheckboxField, FormError — shadcn bindings
  submit-button.tsx   # submit gate (aria-disabled, markup contract)
```

`useAppForm` is the standard for every surface with free-form inputs
(docs/30 §6); hand-rolled `useState` field stacks are legacy and get migrated
when touched.

**Migrated surfaces** (RUN-222): the pool wizard (create + edit), onboarding
(admin credentials and initial-pool steps), the auth profile modal, login,
and the settings constraints form. Onboarding steps 2–3 (provider /
safeguards) intentionally stay on local state — their values have no wire
annotations and no duplicated client rules; they migrate if they ever grow
validation needs. The logs filters (phase 4, optional) were skipped: filter
inputs have no wire rules to share and gating there buys nothing.

Class A rules (protovalidate on the request message) evaluate on blur and
submit; class C rules (UI policy with no wire annotation — the 12-character
admin password policy, confirm-match, retention ranges, custom swap/pids
mode pairing) live in the form layer only. Forms composing the toolkit set
`noValidate` so native HTML5 constraints (min/max/required) never silently
shadow the shared violation map.

### 9.2 Uniform behaviors

- **Timing** (docs/30 §5.5): text/number inputs evaluate on `onBlur` and on
  submit; selects/checkboxes on change; cross-field rules re-evaluate when any
  member changes; the server round-trip re-maps violations on catch.
- **Markup contract** (§5.6): `Field[data-invalid]`, `Input[aria-invalid]`,
  `aria-describedby`, and `FormError[data-testid="form-error"][role=alert]`.
- **One violation map**: the same evaluation feeds inline field errors, step
  gating, and the submit gate — gate and messages cannot disagree.
- **Server round-trip**: `ConnectError` details of type `buf.validate.Violations`
  re-enter the identical field mapping (keyed by the last field-path element and
  stable `rule_id`s from `violations.ts`); anything unmapped falls back to the
  error banner. Banner text always mirrors the mapped messages so cross-step
  rejections stay visible.
- **Class C rules** (UI-state with no wire representation — e.g. custom swap
  mode requires a value, RUN-147) live in the form layer, never as data-format
  re-implementations.

### 9.3 Test expectations

- jsdom (Vitest): blur shows the inline error; submit/step gating blocks;
  server detail rejections map inline/banner; the RUN-147 custom-swap-empty
  case cannot reach the server.
- E2E (Playwright): block-advance and inline-error assertions in the wizard
  specs (`08-pool-edit-workflow.spec.ts`).

## 10. Table Toolkit (TanStack Table v9, RUN-225–228 / docs/31)

Every table renders through the shared toolkit in `web/src/lib/tables/`
(docs/31 §4); hand-rolled `TableHeader`/`TableBody` stacks in route files are
legacy and get migrated when touched. The headless core is
`@tanstack/react-table` v9 (composable feature API) with markup staying in
the shadcn `ui/table.tsx` primitives via the `DataTable` shell.

### 10.1 Toolkit layout (`web/src/lib/tables/`)

```
lib/tables/
  use-app-table.ts     # createTableHook → useAppTable; shared feature set
                       #   (core + rowPagination + rowSorting + sortedRowModel)
  data-table.tsx       # <DataTable table empty getRowProps> shell: flexRender
                       #   headers/cells, default + slot empty states
  sortable-header.tsx  # <SortableHeader column> asc/desc/reset affordance
  index.ts             # public surface
```

Conventions: column defs are factories or module constants using
`createColumnHelper<AppTableFeatures, TRow>()` (two type args in v9),
colocated in a sibling `*-columns.tsx`; per-column cell/header classes come
from typed `columnDef.meta.cellClassName` / `headerClassName`; row-level
attributes (testids, click handlers, selection highlight) flow through
`DataTable`'s `getRowProps`; all table hooks live at the component top level —
never inside conditionally rendered tab JSX.

### 10.2 Data authority

Paging and filtering stay server-authoritative. Job history uses manual
offset pagination (`manualPagination`, `pageCount` from `totalCount`,
bespoke footer driving the query); removal records stay on the accumulated
`useInfiniteQuery` pages with the Load-more button; every other table renders
the server-capped list as-is. Filters always write through to query keys —
the client never slices or widens server results locally.

### 10.3 Client-side sorting (docs/31 §4.4)

Opt-in per column via the `SortableHeader` header renderer: history (Started
At, Completed At, Duration, Queue Wait) and pool runners (State, Uptime).
Sorting is client-side over the currently displayed rows only, cycles
asc → desc → reset, and never changes the server query. All other columns and
tables keep static headers.

## 11. Accessibility Conventions (RUN-255, docs/36)

The product targets WCAG 2.2 AA with automated enforcement: the E2E suite
fails on serious/critical axe violations across the full route × role ×
theme matrix, and `web/src/test/axe-composites.test.tsx` axe-scans the
shared composites in vitest (both gates in docs/13 §3.4). The conventions
below are what those gates assume; new UI that follows them passes.

- **Heading outline:** every page root is one `<h1>` (per-route `usePageTitle`
  supplies the document title; the visible h1 stays in the page).
  `CardTitle` renders `<h2>` — cards sit directly under the page h1, so
  card titles must not skip a level. Sections inside a dialog nest under
  the dialog title (Base UI renders `DialogTitle` as `<h2>`), so wizard
  step headings are `<h3>`.
- **Naming:** icon-only controls carry an accessible name — `aria-label`
  on the control (the LogTerminal filter input) or visible text in the
  toggle (Pause, Auto-scroll). A `placeholder` is never a name.
- **Live regions:** route changes announce through one polite
  `role="status"` region in the app shell; failed submissions (login,
  passkey, wizard step) render `role="alert"` banners. The LogTerminal
  viewport is `role="log"` with `aria-live="off"` **permanently** (docs/36
  §5.4): streaming stdout must not be dictated; users pause and read.
  Do not enable polite streaming.
- **Tables:** sortable headers cycle asc → desc → reset and the shell
  publishes `aria-sort` on every header cell (§10.3); action columns use
  sr-only header names.
- **Charts:** decorative SVGs are `aria-hidden` and their card carries an
  sr-only current-state summary computed from the same query data;
  tooltips are never the only carrier of information (badge descriptions
  duplicate as sr-only text).
- **Forms:** every field pairs `aria-invalid` with `aria-describedby` →
  the inline error (`role="alert"`) — the docs/30 §5.6 markup contract,
  which `ui/field.tsx` implements. Submit buttons set `aria-disabled`
  while gated and `aria-busy` while submitting. Credential fields declare
  `autoComplete` (`username`, `current-password`, `new-password`).
- **Focus:** `:focus-visible` rings on everything interactive; dialogs
  open with focus on the first meaningful control, trap it, restore it on
  close, and failed wizard steps focus the first invalid field.
- **Theme tokens:** text and icons use AA-tuned tokens (`--link` for
  accent text/icons, per-theme `--muted-foreground`/`--destructive`);
  alpha-diluted small text (`/70`–`/90`) cannot pass 4.5:1 and is not
  used. The LogTerminal is theme-independent (`terminal-*` tokens only).
