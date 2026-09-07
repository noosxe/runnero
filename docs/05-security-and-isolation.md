# Security & Isolation Guardrails

GitHub, Gitea, and Forgejo runners execute untrusted workflow code. Security is our absolute priority. The supervisor and dynamic runners incorporate strict isolation features to minimize the attack surface.

## 1. Non-Root Context

Runner containers spawned by the supervisor must execute jobs under a dedicated low-privilege system user (e.g., `runner` UID `1001`), as defined in the base `Dockerfile`. 
The `act_runner` process (for Gitea), `forgejo-runner` process (for Forgejo), and the `run.sh` process (for GitHub) must not be executed as `root`.

## 2. Credential Segregation & Token Generation

Since runner registration tokens expire quickly (typically 1 hour), a long-running supervisor cannot rely on static tokens. It must authenticate dynamically:

- **GitHub App / PAT**: The supervisor loads the private key or PAT. It requests an installation token, and then requests a fresh **Runner Registration Token** via the GitHub API. If spawning a Renovate task container, it simply passes the short-lived installation token itself.
- **Gitea / Forgejo PAT**: The supervisor calls Gitea's / Forgejo's actions runner token API to retrieve a fresh Runner Registration Token.

**Strict Segregation**: Tokens are generated on-the-fly and passed strictly via environment variables (`RUNNER_TOKEN` or `RENOVATE_TOKEN`) to individual ephemeral containers. The master credentials (private keys, supervisor PATs) are **never** shared with or mounted into the ephemeral containers.

### GitHub App Scopes
To support the AIO Supervisor *and* Renovate Bot, the GitHub App must be provisioned with the following permissions:
- **Runner Registration**: `Administration: read`, `Metadata: read`
- **Renovate Execution**: `Contents: write`, `Pull requests: write`, `Workflows: write` (if updating actions)

## 3. Resource Quotas & Saturation

Every runner pool allows specifying CPU and memory boundaries (e.g., `cpus: "2.0"`, `memory: "4g"`) directly in its configuration. The supervisor passes these constraints to the container engine, preventing a single rogue workflow from consuming all host system resources and causing a denial of service. 

Additionally, the `Total Allowed Runners` global limit acts as a circuit breaker, queuing internal provisioning if the host reaches maximum capacity.

## 4. Docker Socket Isolation (DooD Safety)

By default, the host Docker socket (`/var/run/docker.sock`) is **not** mounted into runner containers.
- If a repository strictly requires building Docker images or running containerized actions, the `allow_docker: true` flag must be explicitly set for that pool.
- Gitea's `act_runner` and Forgejo's `forgejo-runner` inherently rely on Docker-in-Docker (DooD) to run workflows. Configuring `allow_docker: true` is strictly required for Gitea and Forgejo. The Web UI enforces this dependency and will prevent users from saving a Gitea or Forgejo pool configuration without Docker access enabled. However, because this exposes the host socket to the runner, sibling container privileges must be carefully considered for untrusted repositories.

> **Current Scope & Trust Boundary**: Gitea and Forgejo integrations are designed for **private/internal** instances only. The mandatory `allow_docker: true` requirement for these providers is an accepted trade-off within this trust boundary — untrusted third-party workflow code is not expected to run on these pools. Public GitHub is the only provider expected to handle untrusted workflow code, and Docker socket access remains opt-in for GitHub pools.
>
> **Future Mitigation** *(deferred)*: Rootless Docker, Podman support, and Sysbox runtime integration are tracked as future enhancements to reduce socket exposure for all providers.

## 5. Configuration & Database Security

- **Encryption at Rest & Secret Derivation**: Sensitive credentials stored in the local SQLite database must be encrypted at rest using AES-256. The AES key and the JWT signing secret are both deterministically derived from the single required `SUPERVISOR_DB_ENCRYPTION_KEY` master key via HKDF-SHA256, each under its own context label, so the two secrets are cryptographically independent while operators manage exactly one secret (implemented in `internal/keys`, RUN-9).
- **Local Administrator Hashing**: The initial local administrator password must be securely hashed (using algorithms like bcrypt or argon2) prior to storage in the SQLite database to protect against offline attacks.
- **Export/Import Sanitization & Zero Secret Leakage**:
  - **YAML Export Sanitization**: When exporting configurations (`supervisor export` CLI or future UI export) for GitOps, backup, or inspection, all credential material is sanitized before serialization:
    - **Private Keys**: Replaced with the explicit placeholder `${REDACTED}` (`db.SanitizedRedactedPlaceholder`). Raw PEM or DER blocks are never emitted.
    - **Tokens & PATs**: Replaced with symbolic environment variable references (e.g., `token_env_var: SUPERVISOR_<NAME>_TOKEN`, `gitea_token_env_var`, `forgejo_token_env_var`). Plaintext token values are never emitted.
    - **File System Permissions**: Export files written by the CLI are restricted strictly to `0600` (`-rw-------`) mode.
    - **Merge Mode Preservation**: Re-importing sanitized YAML into an existing instance via `merge` mode (`supervisor import --mode merge`) automatically preserves existing encrypted credentials in SQLite if the incoming configuration contains placeholders or empty credential fields, ensuring GitOps pool property updates do not inadvertently clear stored credentials.
  - **API Wire Protocol Sanitization**: RPC definitions in `proto/api.proto` strictly omit raw secrets from read schemas. The `AuthProfile` proto message contains only boolean presence flags (`has_private_key`, `has_token`) and write-only inputs during creation. Responses across all RPC endpoints (auth, pools, profiles, onboarding, analytics) are cryptographically free of plaintext credentials, password hashes, or session token hashes.
  - **Negative Leakage Verification**: Continuous integration verifies zero secret leakage via negative scanning test suites (`internal/db/leakage_test.go`, `cmd/supervisor/export_test.go`, `internal/server/leakage_test.go`), which seed realistic secrets (RSA PEM keys, classic and fine-grained PATs, provider tokens, password hashes) and scan all outputs (YAML files, stdout, and binary/JSON RPC responses) to ensure complete secret absence.
- **Session Tokens**: Admin session tokens are JWTs transported exclusively via `HttpOnly` secure cookies with `SameSite=Strict` policy. In production behind a TLS reverse proxy, setting `SUPERVISOR_SECURE_COOKIE=true` (or `--secure-cookie`) attaches the `Secure` flag, guaranteeing cookies are never transmitted over plaintext HTTP. Raw tokens are never exposed to client-side JavaScript, mitigating XSS-based token theft. Session state is tracked in the database for audit and forced revocation capabilities.

## 6. Transport Security & Reverse-Proxy TLS Termination

The supervisor daemon listens exclusively on plain HTTP (`SUPERVISOR_PORT`, default `8090`) and delegates TLS termination, certificate renewal, and port 80/443 binding to an external reverse proxy (Caddy or Traefik) per the architectural decisions in [open-questions.md](open-questions.md#question-25-tls-termination-for-web-ui).

- **No Self-Signed Certificates**: Avoids certificate errors, trust establishment hurdles, and TLS library bloat inside the container.
- **Unbuffered ConnectRPC Streaming**: Proxies must disable response buffering (e.g. Caddy `flush_interval -1`, Traefik `flushInterval: -1`) to support real-time runner log tailing (`StreamRunnerLogs`) and live dashboard watch feeds (`WatchDashboard`).
- **HTTP/2 Multiplexing**: Proxies terminate TLS and negotiate HTTP/2 (or HTTP/3), circumventing browser 6-connection limits per host for concurrent streaming RPCs.
- **Reference Configurations**: Complete production setups with copy-paste Caddy and Traefik Compose manifests are documented in [docs/10-reverse-proxy-tls.md](10-reverse-proxy-tls.md).

## 7. In-Container Elevation (Passwordless Sudo)

For parity with GitHub-hosted runners, the runner image grants the `runner`
user **passwordless sudo** (`/etc/sudoers.d/90-runner` with `NOPASSWD:ALL`, plus
`sudo` group membership; validated by `visudo -c` at build time). This mirrors
the hosted-runner contract that community workflows depend on (`sudo apt-get
install …` in setup steps) and is scoped strictly to inside the ephemeral
runner container:

- **Trust boundary unchanged**: workflow code already executes as `runner`; the
  decision to trust a repository with a runner pool is made by the supervisor
  administrator. Sudo widens only the *in-container* blast radius.
- **No escape path**: sudo cannot grant capabilities outside the container's
  bounding set — `cap_drop` hardening (§4 guidance) remains fully effective, and
  no new host mounts or socket access are introduced.
- **Ephemerality limits persistence**: elevated modifications (packages,
  `/actions-runner` contents, sudoers) live only for the container's single-job
  lifetime; containers are pruned on completion and deregister on `SIGTERM`.
- **Deliberate exclusions**: `openssh-server` and other listening daemons are
  not preinstalled; workflows needing them install them per job via sudo.
- **Deployment caveat**: do not set `security_opt: ["no-new-privileges:true"]`
  on the runner service — setuid-based `sudo` requires privilege transitions
  and would break workflow parity. The default `docker-compose.yml` is
  compatible.

Package-parity scope and the full tiering rationale are documented in
[docs/18-runner-image-package-parity.md](18-runner-image-package-parity.md).
