# 39. NixOS Module: Native Supervisor Service

| | |
| :--- | :--- |
| Related | docs/02 (architecture) · docs/05 (security & isolation) · docs/10 (reverse proxy) · docs/11 (CI/CD) · docs/26 (embedded Tailscale) · docs/28 (log persistence) · docs/backup-and-recovery |
| Touches | `flake.nix` (outputs only), new `nix/package.nix`, `nix/module.nix`, `nix/tests/module.nix`, new `VERSION`, `.github/workflows/release.yml` (one guard step), new `.github/workflows/nix.yml`, README, AGENTS.md (gates, hash procedure, release `VERSION` bump), docs/02, docs/11, docs/backup-and-recovery |

## 1. Problem

Runnero ships exactly one deployment shape: the supervisor as a container
(`Dockerfile.supervisor`, s6-overlay inside) next to the host Docker engine.
On NixOS that means running a Compose stack outside the system configuration:
the supervisor's version, settings, port, firewall rule, and secrets live in a
`.env` file and an image tag rather than in the declarative system config that
manages everything else on the host. There is no `services.runnero` to
enable, nothing to roll back with a generation, and the supervisor's logs live
in Docker rather than the journal.

The flake today only provides the dev shell (`nix/devshell.nix`).

## 2. Decision

Ship a **system-level NixOS module** (`nixosModules.default`) that runs the
supervisor **natively as a systemd service**, built from source by the flake.

- **System level, not Home Manager.** The supervisor is a boot-time daemon
  holding durable state (SQLite, backups, logs, Tailscale node state),
  listening on a port, and talking to the system Docker engine. Home Manager
  manages per-user environments; its user services stop at logout unless
  lingering is enabled, and the Docker socket is root-equivalent anyway
  (README "Docker daemon socket"), so a per-user deployment buys no
  isolation — it only hides a root-equivalent service under a user account.
- **Native, not a wrapped OCI image.** The *runners* are the containers; the
  supervisor does not need to be one. Nothing in the daemon assumes a
  container: it is a CGO-free Go binary with the SPA embedded (`web/embed.go`),
  it reaches the engine through `docker-host` / `DOCKER_HOST`, and its only
  container-flavored default is `data-dir = /data`, which the module
  overrides. The two jobs s6 does in the image map directly onto systemd:
  `init-socket-perms` (join the socket's group) becomes
  `SupplementaryGroups=docker`, and `s6-setuidgid supervisor` becomes `User=`.
  A `virtualisation.oci-containers` wrapper would be Compose in Nix syntax —
  s6 inside, image tags to pin, Docker-managed logs — with none of the
  benefits below.
- **Runner images are still pulled from the registry.** The runner image is
  an Ubuntu 24.04 userland by design (hosted-runner package parity, docs/18;
  passwordless sudo so workflows can `apt-get install`), which `dockerTools`
  cannot reproduce, and an activation-time `docker build` would be impure and
  networked. The image is also per-pool database state (default
  `ghcr.io/noosxe/runnero:latest`), already pulled and update-checked by the
  supervisor (`internal/server/image_update.go`). The module does not touch
  it.
- **Distributed only from this flake.** No nixpkgs submission.
- **Docker only.** Podman and rootless runtimes remain unsupported (docs/05
  §4); the module asserts Docker.

What the module gains over Compose on NixOS: the supervisor version is the
flake revision in `flake.lock` (rollback = previous generation), settings are
typed Nix options, logs go to the journal, the firewall rule is declarative,
and secrets come from whatever secret manager the host already uses.

### Non-goals

- A Home Manager module, nixpkgs upstreaming, nix-darwin support.
- Podman / rootless Docker / socket-proxy provisioning (a socket proxy keeps
  working as today — point `docker-host` at it, §3.3).
- Building or pre-pulling runner images.
- Managing the encryption key or any other secret (§5.1): the module consumes
  a secret file, it never creates one.
- Reverse-proxy configuration: the consumer uses the existing NixOS
  `services.caddy` / `services.nginx` / `services.traefik` modules, following
  docs/10.
- Automating hash updates on dependency bumps (§6).
- Any Go, proto, or frontend change. The daemon is consumed exactly as it is.

## 3. Architecture

### 3.1 Flake layout

`flake.nix` stays minimal: inputs plus wiring. Every definition lives under
`nix/`:

```
flake.nix                 inputs + outputs wiring only
nix/devshell.nix          (existing) dev shell
nix/package.nix           supervisor package (frontend + Go build)
nix/module.nix            NixOS module
nix/tests/module.nix      NixOS VM test
```

New outputs:

| Output | Systems | Notes |
| :--- | :--- | :--- |
| `packages.<system>.runnero-supervisor`, `packages.<system>.default` | `x86_64-linux`, `aarch64-linux` | The supervisor binary. |
| `nixosModules.runnero`, `nixosModules.default` | — | The module (§3.3). |
| `checks.x86_64-linux.nixos-module` | `x86_64-linux` | VM test (§7). |

`devShells` keep `eachDefaultSystem` (macOS contributors still need the dev
shell); the package and module outputs are Linux-only because the daemon is.
The module is built as a function of `self` so its `package` default can
reference the flake's own build.

### 3.2 Package (`nix/package.nix`)

Two derivations, mirroring the two build stages of `Dockerfile.supervisor`:

1. **Frontend** — `stdenvNoCC.mkDerivation` with `pnpmConfigHook` +
   `pnpmBuildHook`, `pnpmDeps = fetchPnpmDeps { fetcherVersion = 5; pnpm =
   pnpm_11; hash = …; }`, `pnpmRoot = "web"`, `nodejs_24`. Output: the built
   `web/dist`. `pnpm_11` matches the image's pinned pnpm major and lockfile
   format 9.0; `fetcherVersion` 5 is the current one (versions 1–2 were removed
   in 26.11 and 3 refuses pnpm ≥ 11).
2. **Supervisor** — `buildGo127Module` (the pinned nixpkgs' default `go` is
   1.26; `go.mod` requires 1.27.3, the same pin the dev shell carries):
   - `src` filtered with `lib.fileset` to `go.mod`, `go.sum`, `cmd/`,
     `internal/`, `proto/`, `web/embed.go` — so unrelated edits (docs, tests,
     workflows) do not rebuild it;
   - `preBuild` copies the frontend output into `web/dist` for `go:embed`;
   - `subPackages = [ "cmd/runnero-supervisor" ]`, `env.CGO_ENABLED = 0`,
     `ldflags = [ "-s" "-w" "-X main.version=${version}" ]`;
   - `vendorHash = …`;
   - `doCheck = false`: the Go suite already runs in `go.yml`, and the VM
     test (§7) covers the package as deployed;
   - `meta.mainProgram = "runnero-supervisor"`, `meta.platforms =
     lib.platforms.linux`.

`version` comes from a new root-level `VERSION` file (§3.6), since a flake
cannot see git tags.

### 3.3 Module (`nix/module.nix`) — options

Everything under `services.runnero`:

| Option | Type | Default | Purpose |
| :--- | :--- | :--- | :--- |
| `enable` | bool | `false` | |
| `package` | package | the flake's `packages.<system>.default` | Override to use a different build. |
| `settings` | freeform YAML (`pkgs.formats.yaml`) | see below | Rendered to a config file passed as `--config`. Keys are the existing config-file keys (`internal/config/config.go` `envKeys`), so the doc for every knob is the existing docs. |
| `environmentFile` | `pathWith { inStore = false; absolute = true; }` | — (required) | Read by systemd as root, loaded into the daemon's environment. Carries `SUPERVISOR_DB_ENCRYPTION_KEY` and any other secret (§5.1). |
| `openFirewall` | bool | `false` | Opens `settings.port` on TCP. |

Typed sub-options on `settings` (all other keys pass through freeform):

| Key | Default | Note |
| :--- | :--- | :--- |
| `port` | `8090` | Same default as the image. |
| `log-level` | `"info"` | |
| `docker-host` | `"unix:///run/docker.sock"` | NixOS's socket path; a `tcp://` value (socket proxy) is allowed. |
| `data-dir` | `"/var/lib/runnero"` | **Read-only option**: it must equal the unit's `StateDirectory` so ownership and permissions are managed by systemd. |

Why `environmentFile` uses `pathWith { inStore = false; }`: a Nix path literal
(`./secrets.env`) is copied into the world-readable `/nix/store` before the
module sees it. The type rejects store paths at evaluation time, so the only
accepted value is a string naming a file on the host (e.g. a sops-nix or
agenix output under `/run`).

Assertions (evaluation-time errors):

- `environmentFile` is set — the daemon refuses to start without
  `SUPERVISOR_DB_ENCRYPTION_KEY` (32-byte minimum) anyway; failing at
  evaluation is earlier and clearer.
- `settings` carries none of the secret keys `db-encryption-key`,
  `webhook-github-secret`, `webhook-gitea-secret`, `webhook-forgejo-secret`,
  `tailscale-auth-key`. The rendered config file is in the store, so these
  would leak to every local user; the message points to `environmentFile`.
- `virtualisation.docker.enable` is true when `docker-host` is a `unix://`
  socket. A `tcp://` host (socket proxy, remote engine) skips the assertion.

### 3.4 Module — systemd unit and user

A **static system user and group `runnero`** (`isSystemUser`), not
`DynamicUser`: the admin wrapper (§3.5) runs CLI subcommands as the same
identity against the same database, and a stable UID keeps backups and
restores of `/var/lib/runnero` simple.

`systemd.services.runnero`:

- `ExecStart = "${lib.getExe cfg.package} daemon --config ${configFile}"`
- `EnvironmentFile = cfg.environmentFile`
- `User = group = "runnero"`, `SupplementaryGroups = [ "docker" ]` (only
  when the socket is local)
- `StateDirectory = "runnero"`, `StateDirectoryMode = "0750"`, `UMask = "0027"`
  — the data dir holds the database (credentials encrypted at rest), backups,
  runner log captures, and Tailscale node keys (docs/26 keeps its state under
  the data dir by default).
- `wantedBy = [ "multi-user.target" ]`; `after` / `wants`
  `network-online.target` (forge APIs, tsnet login); `after` + `wants`
  `docker.service` and `docker.socket`.
  **`wants`, not `requires`**: a Docker outage is a handled state (`/readyz`
  answers `degraded`, docs/02 §5); `requires` would bounce the supervisor on
  every engine restart for no benefit.
- `Restart = "on-failure"`, `RestartSec = "5s"`.
- `TimeoutStopSec = "30s"`: the daemon bounds its own shutdown at 10 s
  (`daemonShutdownTimeout` in `cmd/runnero-supervisor/daemon.go`); 30 s
  leaves margin before systemd escalates to SIGKILL.
- `AmbientCapabilities = CapabilityBoundingSet = [ "CAP_NET_BIND_SERVICE" ]`
  **only** when `settings.port < 1024`; otherwise both are empty.
- Hardening: `NoNewPrivileges`, `ProtectSystem = "strict"`, `ProtectHome`,
  `PrivateTmp`, `PrivateDevices`, `ProtectKernelTunables`,
  `ProtectKernelModules`, `ProtectKernelLogs`, `ProtectControlGroups`,
  `ProtectClock`, `ProtectHostname`, `ProtectProc = "invisible"`,
  `RestrictNamespaces`, `RestrictRealtime`, `RestrictSUIDSGID`,
  `LockPersonality`, `MemoryDenyWriteExecute`, `SystemCallArchitectures =
  "native"`, `SystemCallFilter = [ "@system-service" "~@privileged"
  "~@resources" ]`, `RestrictAddressFamilies = [ "AF_UNIX" "AF_INET"
  "AF_INET6" "AF_NETLINK" ]` (netlink for tsnet's network monitor).
  The final set is whatever the VM test (§7) proves compatible with the
  Docker socket connection and the tsnet boot path; any directive that
  breaks either is dropped, and the drop is noted in the implementation PR.

**Restart on `nixos-rebuild switch`.** NixOS restarts a unit whose definition
changed (`restartIfChanged`, default true), and any package or settings change
changes it. A restart is a normal SIGTERM shutdown (docs/03 §7): idle runners
are deregistered and removed, and busy runners are force-terminated when the
10 s window ends — **in-flight jobs on this host fail**, exactly as with a
Compose container recreate today. The module keeps the NixOS default (config
changes must apply) and documents it; an operator who prefers to schedule
restarts can set `systemd.services.runnero.restartIfChanged = false` in their
own config.

### 3.5 Admin CLI (`runnero-admin`)

The subcommands `reset-password`, `backup`, `export`, `import`, `version`
need the same config file, data dir, and encryption key as the daemon. In the
container they run via `docker exec`. The module adds a small wrapper to
`environment.systemPackages`:

```
runnero-admin <subcommand> [args…]
  → systemd-run --pty --wait --collect --quiet \
      --uid=runnero --gid=runnero \
      -p EnvironmentFile=<environmentFile> \
      -p WorkingDirectory=/var/lib/runnero \
      --setenv=SUPERVISOR_CONFIG=<configFile> \
      <package>/bin/runnero-supervisor <subcommand> [args…]
```

The config file reaches the CLI through `SUPERVISOR_CONFIG` rather than the
root `--config` flag: `import` defines its own local `--config` (the file to
import), which shadows the root flag, so `--config <configFile> import
--config <file>` would silently drop the settings file and fall back to the
`/data` default. The environment variable is the same config layer (docs/02
§4) and leaves every subcommand's own flags untouched.

It must be run as root (or via sudo), which is the right bar for resetting an
admin password or decrypting an export. `systemd-run` means the wrapper never
reads the secret file itself — PID 1 loads it exactly as for the service —
and `--pty` keeps `reset-password`'s interactive prompt working. Concurrent
use with the running daemon follows the existing semantics of each subcommand
(unchanged; docs/backup-and-recovery).

### 3.6 Version file

A flake build only sees the source tree, not git tags, so a build from
`github:noosxe/runnero/v1.1.0` would otherwise report a commit hash. A new
root-level **`VERSION`** file holds the release tag verbatim (e.g.
`v1.1.0`, one line), and it becomes the single source both sides read:

- **Flake:** `nix/package.nix` reads it with `lib.fileContents ../VERSION`.
  The content is stamped into `main.version` unchanged, so
  `runnero-supervisor version` prints exactly what the release image prints
  (`runnero-supervisor version v1.1.0`). The Nix `version` attribute is the
  same string without the leading `v`, per nixpkgs convention.
- **Release workflow:** the `resolve` job in `release.yml` gains one guard:
  the tag being released must equal the content of `VERSION` at the tagged
  commit, otherwise the release fails before any image is built. The images
  keep stamping `VERSION=<tag>` as today, which the guard makes identical to
  the file. Tags cut before the file existed have no `VERSION`; the guard
  skips them with a warning so `workflow_dispatch` re-releases of old tags
  keep working.
- **Release process:** bumping `VERSION` becomes part of cutting a release —
  the bump lands on `main` via a PR, then that commit is tagged. AGENTS.md
  documents the step.

Accepted trade-off: between releases, a flake build from `main` reports the
last released version, not a pre-release marker. Builds from a release tag
— the supported way to consume the flake (§4) — report the exact tag. This
is a trial: if the bump step proves error-prone or the `main` behavior
confuses, the fallback is reporting `self.shortRev` (no file, no process
step); switching only touches `nix/package.nix`, `release.yml`, and this
section.

### 3.7 What stays unchanged

- **Runner networking.** Runners talk to the forge, never back to the
  supervisor; the supervisor-managed bridge network is created through the
  engine as today. No NixOS firewall rule is needed for runners.
- **Tailscale (docs/26).** tsnet is userspace and needs nothing from the
  host; its state lands in `/var/lib/runnero/tailscale` by default. The auth
  key goes in `environmentFile` (`SUPERVISOR_TAILSCALE_AUTHKEY`).
- **Reverse proxy (docs/10).** The consumer fronts `settings.port` with the
  NixOS proxy module of their choice and sets `trusted-proxy` /
  `secure-cookies` as documented.
- **The container images and Compose stack** remain the primary distribution
  for non-NixOS hosts.

## 4. Configuration contract (consumer view)

```nix
# flake.nix of the host configuration
inputs.runnero.url = "github:noosxe/runnero/v1.1.0";

# configuration.nix
{ inputs, config, ... }: {
  imports = [ inputs.runnero.nixosModules.default ];

  virtualisation.docker.enable = true;

  services.runnero = {
    enable = true;
    openFirewall = true;
    environmentFile = "/run/secrets/runnero.env";
    settings = {
      port = 8090;
      log-level = "info";
      backup-retention-count = 14;
      webauthn-rp-id = "runnero.example.ts.net";
    };
  };
}
```

The secret file is a dotenv file, one variable per line:

```
SUPERVISOR_DB_ENCRYPTION_KEY=<openssl rand -base64 32>
SUPERVISOR_WEBHOOK_GITHUB_SECRET=<optional>
SUPERVISOR_TAILSCALE_AUTHKEY=<optional>
```

How it gets onto the host is the consumer's choice. The README will carry one
worked example, for sops-nix's template feature:

```nix
sops.secrets.runnero-db-key = { };
sops.templates."runnero.env" = {
  content = ''
    SUPERVISOR_DB_ENCRYPTION_KEY=${config.sops.placeholder.runnero-db-key}
  '';
};
services.runnero.environmentFile = config.sops.templates."runnero.env".path;
```

Settings precedence is the daemon's existing one (flags > env > config file >
defaults, `config.Load` in `internal/config/config.go`): environment-file
values override `settings`, which is
why secrets work through the environment file without any code change.

## 5. Security implications

### 5.1 Secrets

- **Ownership.** Secret provisioning is the consumer's responsibility. The
  module exposes `environmentFile` and nothing else; it never generates,
  stores, or rotates a key. In particular it does **not** auto-generate the
  encryption key into the data dir: a key stored next to the database it
  protects defeats the encryption for anyone who copies the data dir or its
  backups.
- **No store leaks.** `environmentFile` rejects store paths (§3.3), and
  secret keys in `settings` are an evaluation error. The rendered config file
  is in the store and must be treated as public.
- **At runtime** the key lives in the daemon's environment
  (`/proc/<pid>/environ`, readable by root and the `runnero` user) — the same
  exposure as the container today. The daemon spawns no child processes that
  would inherit it (runners are created through the Docker API with their own
  explicit env). A `*_FILE` / `LoadCredential=` mechanism would narrow this
  further but needs a code change; it is out of scope here and can be filed
  separately.
- **Loss = credential loss.** Losing the key makes every stored PAT, GitHub
  App key, and forge token undecryptable. docs/backup-and-recovery gains a
  note: back up the key separately from `/var/lib/runnero`.

### 5.2 Docker socket

Unchanged in substance: socket access is root-equivalent (README "Docker
daemon socket"). Two differences from the container:

- In the image, the supervisor joins the socket's group via s6 at container
  start. Natively, `SupplementaryGroups=docker` scopes that membership to the
  service: the `runnero` user is **not** added to the `docker` group, so the
  admin wrapper and any other process running as `runnero` do not get engine
  access.
- The native process has no container boundary around it. The systemd
  hardening in §3.4 replaces it for everything *except* the engine — it
  limits what a compromised daemon can do to the host directly, but by
  construction it cannot limit what it can do through the socket. The README
  security note is extended to say so for NixOS deployments.

### 5.3 Supply chain

- The supervisor is built from source pinned by the consumer's `flake.lock`;
  there is no prebuilt binary or image to trust for the supervisor itself.
- Go modules and the pnpm store are fixed-output derivations: their hashes
  pin the exact dependency set; any change in the upstream content fails the
  build.
- The frontend build runs in the Nix sandbox without network. pnpm
  lifecycle scripts follow the existing `allowBuilds` policy in
  `web/pnpm-workspace.yaml` (esbuild only).
- Runner images are still pulled from ghcr.io, governed by the existing
  image-update flow; no change.

## 6. Hash maintenance

`vendorHash` and the pnpm `hash` change whenever `go.sum` or
`web/pnpm-lock.yaml` change, which includes every Dependabot `gomod` / `npm`
bump. **For now they are fixed by hand on those PRs.** This is accepted and
temporary: once the project runs its own runners with Renovate, a
post-upgrade task can regenerate them; that is out of scope here.

Procedure (documented in AGENTS.md):

1. Set the stale hash to `lib.fakeHash`.
2. `nix build .#runnero-supervisor` — the build fails and prints the correct
   hash.
3. Paste it back, rebuild to confirm.

The `nix.yml` CI job (§7) is the guard: a PR that changes `go.sum` or the
pnpm lockfile without updating the hashes fails it.

## 7. Testing strategy

- **Build:** `nix build .#runnero-supervisor` on `x86_64-linux` and
  `aarch64-linux` (the latter on GitHub's arm64 runners), proving both
  hashes and the embedded frontend.
- **VM test** (`checks.x86_64-linux.nixos-module`, `pkgs.testers.runNixOSTest`):
  one node with `virtualisation.docker.enable`, the module enabled, and a test
  environment file written by the test script with a generated key (never a
  committed one). Asserts:
  - `runnero.service` reaches active and `GET /readyz` answers 200 (database
    + pool control loop healthy, Docker reachable through the socket under the
    full hardening set);
  - the process runs as `runnero`, and `runnero` is not in the `docker`
    group;
  - `/var/lib/runnero` is `0750 runnero:runnero` and contains
    `supervisor.db`;
  - `runnero-admin version` prints the package version and
    `runnero-admin backup` writes a snapshot;
  - the generated key string does not appear in the system closure (no store
    leak).
  The test creates no pools, so it pulls no images and needs no network.
  A second node runs headscale (the NixOS `services.headscale` module behind
  nginx with a self-signed certificate) as the tsnet control server; the
  supervisor's environment file carries a headscale pre-auth key and
  `TS_CONTROL_URL`, so the embedded node enrolls under the full hardening set;
  the test asserts it is registered and reaches the `Running` state. The
  daemon then aborts opening its tailnet HTTPS listener because headscale
  cannot issue ts.net certificates (a control-server limit, asserted by its
  error message), so the remaining assertions run in a second phase with the
  auth key removed from the environment file. The control URL uses headscale's VM
  IP: tsnet resolves the control host through DNS, not `/etc/hosts`. Because the
  unprivileged daemon cannot set `SO_MARK`, tsnet binds its control dials to
  the default-route interface, so the test routes the default via the test
  network rather than QEMU's user-mode interface; on a real host the default
  route reaches the control server anyway.
- **Evaluation tests** in the same check: a settings value containing
  `db-encryption-key` and a store-path `environmentFile` both fail evaluation
  with the documented messages.
- **Release guard:** the new `release.yml` check is exercised by
  the first release cut after the implementation lands; a mismatch fails in
  `resolve` before any image is built, so a wrong bump costs nothing but a
  re-tag.
- **CI:** a new `.github/workflows/nix.yml` runs both package builds and
  `nix flake check --no-build` (evaluation only: every output plus the
  evaluation tests above, no VM), behind the same `dorny/paths-filter` gatekeeper pattern as
  the other workflows (docs/11), triggered by `flake.*`, `nix/**`, `VERSION`,
  `go.mod`, `go.sum`, `cmd/**`, `internal/**`, `proto/**`, `web/**` (plus
  the workflow file itself). The package builds also smoke-check that the
  binary reports `runnero-supervisor version <VERSION>`. The VM test is
  **not** run in CI (product-owner decision during implementation): it is a
  local pre-push gate.
- **Local gate:** AGENTS.md adds `nix build .#runnero-supervisor` and
  `nix flake check --no-build` to the pre-push gates for PRs that touch the
  trigger paths above, plus the VM test
  (`nix build .#checks.x86_64-linux.nixos-module`) for PRs that touch the
  flake or the module.

## 8. Implementation plan

One implementation issue, one PR (the pieces are not useful separately):

1. `VERSION` file + `release.yml` guard; `nix/package.nix` + flake outputs;
   hashes; `nix build` on both arches.
2. `nix/module.nix`: options, assertions, user, unit, hardening,
   `runnero-admin`.
3. `nix/tests/module.nix` + `checks` output.
4. `nix.yml` workflow.
5. Docs: README (Features entry, roadmap entry removed, a "NixOS" install
   section with the §4 example, security-note addition), docs/02 (deployment
   shapes), docs/11 (new workflow), docs/backup-and-recovery (native paths,
   `runnero-admin backup`, key backup note), AGENTS.md (gates, hash
   procedure, `VERSION` bump in the release steps).

## 9. Decided follow-ups

- **Version string:** decided — the `VERSION` file (§3.6), as a trial with a
  documented fallback.
- **Runner image version skew:** a native supervisor is pinned to a flake
  revision while new pools default to `ghcr.io/noosxe/runnero:latest`, so the
  supervisor and runner image can drift if their contract changes. This is a
  pre-existing property of the Compose deployment too (supervisor tag vs.
  `:latest`) and stays out of scope here; it is tracked separately as RUN-307.
