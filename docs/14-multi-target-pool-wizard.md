# Multi-Target Runner Pools & Discovery Wizard Specification

## 1. Executive Summary

This document specifies the architecture, data models, protocol extensions, and frontend user experience for **Multi-Target Runner Pools** and the **Multi-Step Pool Creation Wizard** in the `runnero` supervisor.

Currently, runner pools are configured via a single cramped dialog requiring operators to manually copy-paste individual repository or organization URLs. Furthermore, pools are strictly 1:1 with a single repository or organization URL. This design replaces the modal with a 4-step interactive creation wizard that automatically discovers visible entities from the selected Git provider profile and enables a single runner pool to dynamically serve **multiple repositories** or **multiple organizations** concurrently under a shared resource and concurrency ceiling.

---

## 2. Core Requirements & Invariants

### 2.1 Wizard User Experience
1. **Multi-Step Dialog Architecture:** The pool creation dialog is transformed into an intuitive, guided 4-step wizard modal with step indicators and progress tracking:
   - **Step 1: Identity & Credentials:** Pool Name and Git Auth Profile selector.
   - **Step 2: Scope & Target Discovery:** Scope selection (`repo` vs `org`), automatic discovery of visible upstream entities, and multi-selection list with real-time search/filter.
   - **Step 3: Specification & Quotas:** Standby counts (`min_idle_runners`), concurrency limits (`max_concurrency`), architecture-aware runner labels (`self-hosted,linux,<hostArch>`), container images, Docker daemon privileges, resource quotas (CPU/RAM), and Renovate dependency automation.
   - **Step 4: Review & Confirmation:** Comprehensive breakdown of configured targets, limits, and settings before dispatching creation mutations.
2. **Zero URL Copy-Pasting:** The platform queries upstream APIs using the selected Git Auth Profile to discover all accessible organizations and repositories.
3. **Multi-Target Association:** A single pool can target multiple repositories (e.g., `repo-A`, `repo-B`, `repo-C`) or multiple organizations (`org-1`, `org-2`).
4. **Strict Scope Homogeneity:** A pool cannot mix different target types. It must either target multiple repositories (`scope: "repo"`) OR multiple organizations (`scope: "org"`).

---

## 3. Upstream Target Discovery Engine

### 3.1 Provider Discovery Protocols

When the operator selects an authentication profile and scope in Step 2, the supervisor queries the upstream Git provider using the profile's decrypted credentials:

| Provider Auth Method | Scope: `org` (Organizations) | Scope: `repo` (Repositories) |
| :--- | :--- | :--- |
| **GitHub App** | Query `GET /app/installations` (filter `account.type == "Organization"`) | Query `GET /app/installations` → exchange installation token → `GET /installation/repositories` across installations |
| **GitHub PAT** | Query `GET /user/orgs` | Query `GET /user/repos?affiliation=owner,collaborator,organization_member` |
| **Gitea PAT** | Query `GET /api/v1/user/orgs` | Query `GET /api/v1/user/repos` |
| **Forgejo PAT** | Query `GET /api/v1/user/orgs` | Query `GET /api/v1/user/repos` |

### 3.2 Discovery Caching & Rate-Limiting
- Discovery requests pass through the supervisor's existing provider rate-limit transport (`provider.NewRateLimitTransport`).
- Target lists are cached in memory for 60 seconds per auth profile to avoid redundant upstream API requests during wizard navigation.

---

## 4. Multi-Target Orchestration & Lifecycle

A single runner process (GitHub Actions runner, Gitea `act_runner`, or Forgejo `forgejo-runner`) can only be registered to **one** specific URL (`--url <target>`) at a time. Multi-target pools operate under the following orchestration rules:

```mermaid
graph TD
    subgraph Multi-Target Pool (e.g. 3 Repos, max_concurrency = 5)
        P[Pool Controller]
        T1[(repo-alpha)]
        T2[(repo-beta)]
        T3[(repo-gamma)]
    end
    
    W[Webhook: workflow_job.queued for repo-beta] --> P
    P -->|Check Active < Max Concurrency| S[Spawn Runner for repo-beta]
    S -->|Inject Ephemeral Token for repo-beta| C[Runner Container]
    C -->|Execute Job & Exit| E[Destruction & Reaping]
```

1. **Scale-to-Demand per Target:** When a `workflow_job.queued` webhook or polling trigger arrives for a specific target URL associated with the pool:
   - The supervisor checks if total active runners for the pool < `max_concurrency`.
   - If capacity is available, it requests a fresh short-lived registration token specifically for that target URL and spawns an ephemeral container.
2. **Warm Idle Pool Allocation:**
   - If `min_idle_runners > 0`, the pool maintains warm standby containers. Standby runners are distributed evenly across the pool's associated targets (e.g., round-robin allocation) up to the pool's idle ceiling.
   - For scale-to-zero configurations (`min_idle_runners = 0`), zero idle containers are held, and runners spin up strictly on-demand.

---

## 5. Database Schema Changes

A new Goose migration (`002_multi_target_pools.sql`) introduces the `pool_targets` table:

```sql
-- Join table for multi-target pools
CREATE TABLE pool_targets (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    pool_id INTEGER NOT NULL REFERENCES runner_pools(id) ON DELETE CASCADE,
    target_url TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(pool_id, target_url)
);

CREATE INDEX idx_pool_targets_pool_id ON pool_targets(pool_id);
CREATE INDEX idx_pool_targets_url ON pool_targets(target_url);

-- Backfill existing single-target runner pools into pool_targets
INSERT INTO pool_targets (pool_id, target_url)
SELECT id, repository_url FROM runner_pools WHERE repository_url != '';
```

The `runner_pools.repository_url` column remains populated with the primary/first target URL for backward compatibility with legacy tooling and queries.

---

## 6. ConnectRPC Protocols (`proto/api.proto`)

### 6.1 RPC Definitions
Extend `PoolService` with `DiscoverTargets` and update `Pool` message:

```protobuf
service PoolService {
  // Existing RPCs...
  
  // DiscoverTargets queries upstream Git provider for accessible repos or orgs
  rpc DiscoverTargets (DiscoverTargetsRequest) returns (DiscoverTargetsResponse);
}

message DiscoverTargetsRequest {
  int64 auth_profile_id = 1;
  string scope = 2; // "repo" or "org"
  string search = 3; // optional text query filter
}

message DiscoveredTarget {
  string name = 1;        // display name (e.g., "owner/repo" or "organization")
  string url = 2;         // full clone/target URL (e.g., "https://github.com/owner/repo")
  string description = 3; // upstream repository or organization description
  bool is_private = 4;    // private/public visibility flag
}

message DiscoverTargetsResponse {
  repeated DiscoveredTarget targets = 1;
}

message Pool {
  int64 id = 1;
  string name = 2;
  string provider = 3;
  string repository_url = 4; // primary target URL (backwards compatible)
  // ...
  repeated string target_urls = 20; // complete list of target URLs
}
```

---

## 7. Frontend User Interface (`web/src/routes/pools.tsx`)

### 7.1 Wizard Component Architecture
The single dialog is replaced by a dedicated `CreatePoolWizard` component:
- **State Machine:** Steps 1 through 4 (`currentStep: 1 | 2 | 3 | 4`).
- **Target Selection Controls:**
  - Multi-select checkbox card list with sticky search filter bar.
  - "Select All" / "Deselect All" convenience buttons.
  - Badge counter showing currently selected items (e.g., `4 Repositories Selected`).
  - Empty state with retry action if upstream discovery returns zero items or errors.
- **Dynamic Label Suggestions:**
  - Defaults to `getSuggestedRunnerLabels(session?.hostOs, session?.hostArch)` (e.g. `self-hosted,linux,amd64`).

---

## 8. Security & Threat Analysis

1. **Token & Credential Segregation:** The discovery endpoint decrypts credentials within supervisor process memory only. No master credentials, tokens, or private keys are transmitted to the browser.
2. **Access Control:** `DiscoverTargets` is an authenticated ConnectRPC procedure guarded by the supervisor's session cookie interceptor.
3. **SSRF & Host Validation:** Discovered target URLs are validated against the upstream host associated with the auth profile (e.g., a Gitea profile cannot discover or target GitHub URLs).
