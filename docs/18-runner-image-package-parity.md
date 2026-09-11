# 18. Runner Image Package Parity & Passwordless Sudo

> **Status:** Design Phase — awaiting review. No implementation in this PR.

## 1. Problem Statement

The `runnero` unified runner image currently installs a minimal apt set (~14 CLI
tools + Docker CLI) chosen for image size, not workflow compatibility. Workflows
ported from GitHub-hosted runners routinely fail on `runnero` because:

1. **Missing build tooling** — `gcc`, `g++`, `pkg-config`, `openssh-client`,
   `rsync`, `python` (the command itself), and dozens of other utilities assumed
   present on `ubuntu-24.04` hosted runners are absent.
2. **No sudo** — hosted runners execute as user `runner` with **passwordless
   sudo**; a huge fraction of community workflows invoke `sudo apt-get install …`
   during setup steps. On `runnero` this fails immediately (`sudo: command not
   found`), and there is no documented workaround short of shipping the package
   in the image.

**Goal:** achieve package parity with GitHub's `ubuntu-24.04` hosted runner
image for everything that is reasonable in a slim, multi-arch container, and
enable passwordless sudo for the `runner` user — while explicitly documenting
what is deliberately excluded and why.

## 2. Research: What the GitHub `ubuntu-24.04` Image Contains

The canonical source is [`actions/runner-images`](https://github.com/actions/runner-images).
The image is assembled from:

| Source | Inventory | Size class |
| :--- | :--- | :--- |
| `toolset-2404.json` → `apt.vital_packages` | 9 packages (`bzip2 curl g++ gcc make jq tar unzip wget`) | MBs |
| `toolset-2404.json` → `apt.common_packages` | 31 packages (dev headers, VCS, compression, locales, `python-is-python3` …) | tens of MBs |
| `toolset-2404.json` → `apt.cmd_packages` | 34 packages (`acl aria2 binutils bison file flex … sudo … zip`) | tens of MBs |
| Per-tool `install-*.sh` scripts | apt extras (`zstd`, `kubectl`, browsers, DB servers…) | varies |
| Upstream toolchains (`toolcache`) | Python, PyPy, Node, Go, Ruby, CodeQL — multiple versions each | GBs |
| Upstream SDKs & apps | .NET SDKs, Android SDK, Chrome/Edge/Firefox, MS SQL, MySQL, PostgreSQL, MongoDB, Homebrew, Mono, PowerShell, Azure/AWS/GCloud CLIs … | tens of GBs |
| User & elevation model | user `runner`, **passwordless sudo** baked into the base VHD | — |

The apt portion of the toolset (74 packages) is the sane parity target. The
upstream toolchain/s SDK inventory is what makes hosted images ~30–80 GB and is
**not** a reasonable fit for a slim multi-arch runner container (see §4, Tier 3).

### 2.1 Elevation model on hosted runners

Hosted images run workflows as user `runner` with `/etc/sudoers.d` granting
`NOPASSWD:ALL`. Ubuntu's stock sudoers only gives `%sudo` members **passworded**
elevation, so parity requires an explicit sudoers drop-in plus membership in the
`sudo` group. Workflows rely on this for `sudo apt-get install`, `sudo tee`,
kernel-ish tweaks inside their job sandbox, etc.

## 3. Design

### 3.1 Tiered scope

| Tier | Content | Decision |
| :--- | :--- | :--- |
| **1 — apt parity** | The 74 `toolset-2404.json` apt packages, minus 6 container-inappropriate ones (§3.2), deduplicated against our existing set (~12 overlap) | **Implement** — net ~56 new packages |
| **2 — passwordless sudo** | `sudo` package, `runner` in `sudo` group, `/etc/sudoers.d/90-runner` with `NOPASSWD:ALL`, validated by `visudo -c` at build time | **Implement** |
| **3 — upstream toolchains & apps** | Node/Python/Go/Ruby/CodeQL toolcaches, .NET/Android SDKs, browsers, DB servers, cloud CLIs, Homebrew | **Out of scope** — workflows install per-job via `setup-*` actions (they cache into `RUNNER_TOOL_CACHE`, which we provide, §3.4) |
| **4 — toolchain env & dirs** | `ImageOS=ubuntu24`, `RUNNER_TOOL_CACHE=/opt/hostedtoolcache` (runner-owned) so `actions/setup-*` behave identically | **Implement** (cheap, high compat value) |

### 3.2 Tier-1 package manifest (as-built classification)

New file **`src/runner-parity-packages.txt`** — one package per line, `#`
comments allowed, grouped by upstream list with the rationale for every drop.
Canonical contents (68 packages after drops; ~12 already present in the
Dockerfile and deduped by the installer):

- **From `vital_packages` (9):** `bzip2 curl g++ gcc jq make tar unzip wget`
  - already present: `curl jq make tar unzip wget`
- **From `common_packages` (31):** `autoconf automake dnsutils dpkg-dev fakeroot
  iputils-ping libicu-dev libsqlite3-dev libssl-dev libtool libyaml-dev locales
  mercurial openssh-client p7zip-rar pkg-config python-is-python3 rpm texinfo tk
  tree tzdata upx xz-utils zsync` (plus present: `dpkg gnupg2≡gnupg iproute2`)
- **From `cmd_packages` (34):** `acl aria2 binutils bison brotli file flex ftp
  libnss3-tools lz4 m4 mediainfo netcat net-tools p7zip-full parallel patchelf
  pigz rsync shellcheck sphinxsearch sqlite3 sshpass sudo swig telnet time`
  (plus present: `coreutils findutils zip`)
- **apt extras installed by upstream scripts:** `zstd`

**Dropped (6) — container-inappropriate, each annotated in the manifest:**

| Package | Why dropped |
| :--- | :--- |
| `dbus` | System message-bus daemon; no systemd/dbus session in the container |
| `fonts-noto-color-emoji` | Browser-rendering font; no browsers shipped |
| `haveged` | Entropy daemon; irrelevant in containers on modern kernels |
| `pollinate` | First-boot TLS entropy seeding for cloud VMs; N/A in containers |
| `ssh` | Metapackage pulling **openssh-server** — a listening daemon in the runner container; workflows can `sudo apt-get install` it ad hoc |
| `systemd-coredump` | systemd coredump integration; PID 1 is our entrypoint |

**Kept despite GUI-ish deps:** `tk` (Python `tkinter` parity; ~40 MB), 
`sphinxsearch` (tiny; inert daemon binary), and `xvfb` (re-added by review
decision — some GUI-style workflow suites need `xvfb-run`; §7).

**Arch note:** all kept packages publish `arm64` builds in Ubuntu 24.04
(`p7zip-rar`, `sphinxsearch`, `upx` verified). The installer fails the build if
any package is unavailable for `$(dpkg --print-architecture)` — no silent
arch-skew.

### 3.3 Elevation (Tier 2) mechanics

```dockerfile
# after existing user creation, still as root:
RUN apt-get update && apt-get install -y --no-install-recommends sudo \
    && usermod -aG sudo runner \
    && echo 'runner ALL=(ALL:ALL) NOPASSWD:ALL' > /etc/sudoers.d/90-runner \
    && chmod 0440 /etc/sudoers.d/90-runner \
    && visudo -cf /etc/sudoers.d/90-runner \
    && rm -rf /var/lib/apt/lists/*
```

- `sudo` ships in `cmd_packages` upstream; we surface it explicitly so the
  elevation step is auditable in one RUN layer.
- `visudo -c` at build time guarantees the image can never ship a sudoers file
  that makes `sudo` refuse to run (a classic bricked-image failure).
- The `runner` account keeps `UID 1001`; nothing about the non-root execution
  model of the supervisor process changes — elevation exists for **workflow
  steps only**, plus the entrypoint's one-shot socket-group bootstrap (§3.7).
- A second drop-in, `/etc/sudoers.d/91-runner-envkeep`, whitelists the
  container's runner-configuration environment (`RUNNER_*`, `GITEA_*`,
  `FORGEJO_*`, `GITHUB_*`) from sudo's `env_reset`, so the entrypoint's
  sudo re-entry (§3.7) preserves `RUNNER_TOKEN` and provider URLs.

### 3.4 Toolchain environment (Tier 4)

```
ImageOS=ubuntu24
ImageVersion=24.04.<build-date>
RUNNER_TOOL_CACHE=/opt/hostedtoolcache   # created, chown 1001:1001
```

`actions/setup-node`, `setup-python`, `setup-go` etc. install toolchains into
`RUNNER_TOOL_CACHE` on demand; matching hosted-runner paths removes a whole
class of "works on hosted, fails on self-hosted" issues and keeps Tier-3 content
out of the image while remaining compatible with it.

### 3.5 Maintainability & testing

- `src/runner-parity-packages.txt` — reviewed-by-human manifest; every entry
  traceable to `toolset-2404.json` or an explicit drop.
- `src/install-parity-packages.sh` — strict-mode (`set -euo pipefail`) installer:
  reads the manifest, strips comments/empties, dedupes against `dpkg -s` for
  already-installed packages, single `apt-get install` transaction, cleans lists
  in the same layer (hadolint DL3009).
- **CI drift guard (follow-up, non-blocking):** a workflow job that diffs the
  manifest against upstream `toolset-2404.json` and opens an issue on drift, so
  parity is maintained when GitHub revs the image.
- `make test-scripts` extended: shellcheck + shfmt (already mandatory) plus a
  unit test asserting manifest hygiene — no duplicates, no whitespace, no
  dropped packages re-added, sorted within groups.

### 3.6 Image-size impact

Estimated +250–400 MB uncompressed (~+100–150 MB compressed across both arches),
dominated by `gcc`/`g++`/`libicu-dev`/`tk` dep trees. Acceptable: parity value
far exceeds size; Tier-3 exclusion is what keeps the image two orders of
magnitude smaller than hosted images.

### 3.7 Docker socket access without sudo (as-built, RUN-162)

Pools with docker access bind-mount the host `docker.sock` into runner
containers (`internal/orchestrator/docker`). The socket's owning GID is
host-specific and can never match a static group baked into the image, so the
`runner` user cannot reach the Docker API directly — jobs would need `sudo`
for every `docker` call, unlike GitHub-hosted runners where the runner user is
a member of the host's `docker` group.

The entrypoint closes this gap at container start, before the runner agent
starts (group membership is only evaluated at process creation, and a
non-root process cannot join new groups — `sudo -u runner` from the runner
user is a credential no-op and never re-initialises groups). The fix is a
two-stage re-exec:

1. If `/var/run/docker.sock` is present, read its owning GID (`stat -c %g`).
2. Reuse an existing image group with that GID, or create `docker-sock` with
   it (`groupadd`, falling back to `groupmod` if the name is taken). This is
   container-local `/etc/group` state; the host is never mutated.
3. Re-enter the entrypoint through `sudo -n --` as root, then apply the
   membership (`usermod -aG`) and drop back to the runner user via
   `setpriv --reuid --regid --init-groups`, which builds the full group
   vector from `/etc/group`. Every job shell inherits the socket GID.
   The root stage also restores the runner user's identity environment
   (`HOME`/`USER`/`LOGNAME`/`SHELL`): the sudo re-entry rewrites them to
   root's, and setpriv preserves the environment as-is — without this the
   agent and its job shells would inherit `HOME=/root`.
4. `RUNNER_SOCK_GROUPS_SYNCED` guards against re-entry; the
   runner-configuration environment survives the sudo re-entry via the
   sudoers `env_keep` whitelist (§3.3).

Hard rules:

- **Never `chmod` the socket** — the host owns that inode; containers must not
  mutate host state (least-privilege, docs/05).
- Failures degrade with a loud WARNING (docker jobs then need sudo), never a
  broken container: pools without docker access never touch this path.

The E2E workflow (`.github/workflows/e2e.yml`) runs on a docker-enabled pool
and therefore executes `make test-e2e` **without sudo**, exactly like CI jobs
on GitHub-hosted runners. Note the deployment coupling: this only holds once
the pool's configured runner image contains the updated entrypoint — refresh
the pool image before (or right after) merging, or the first E2E run will
fail its compose steps.

Verified against a real host docker.sock (RUN-162): stage one created
container-local `docker-sock` with the socket's host GID, the final process
ran as `runner` with that supplementary group and read/write access to the
socket, and the runner configuration environment survived the re-entry.

## 4. Security Review

| Concern | Assessment |
| :--- | :--- |
| **Workflow code gains root in-container** | Accepted — identical to GitHub-hosted (`runner` + NOPASSWD). Workflow code already executes as `runner`; sudo widens in-container blast radius only. The trust boundary (supervisor admin vets which repos get pools) is unchanged. |
| **Container escape** | None added. Sudo cannot grant capabilities outside the container's bounding set; `cap_drop` guidance in `docs/05` and compose remains fully effective. No new host mounts; Docker socket isolation (DooD) unchanged. |
| **Persistence** | An elevated workflow can modify `/actions-runner` or the sudoers file **within its own ephemeral container lifetime only**. Containers are single-use (pruned on completion/failure) and deregister on `SIGTERM`; registration tokens are short-lived. |
| **Secret exposure** | No new secrets in the image; env-var visibility unchanged (workflow code could already read its own job env as `runner`). DB/at-rest credential model untouched. |
| **openssh-server** | Deliberately excluded (§3.2) to avoid a listening daemon; passwordless sudo means workflows that need it can install it for their job's lifetime. |
| **Build-time safety** | `visudo -cf` gates the sudoers content; installer fails closed on any missing/arch-missing package. |
| **`no-new-privileges` interplay** | Documented requirement: deployments must not set `security_opt: ["no-new-privileges:true"]` for the runner service (setuid `sudo` would break) — called out in `docs/05` and README examples. |
| **Audit** | Image-level change only; no runtime audit surface added. Existing `auth_profile` / pool audit flows unaffected. |

## 5. Files Touched (Implementation Sketch)

```
Dockerfile                        # Tier 1 call + Tier 2 sudoers layer + Tier 4 env
src/runner-parity-packages.txt    # NEW: canonical, annotated package manifest
src/install-parity-packages.sh    # NEW: strict-mode manifest installer
tests/unit/test_parity_packages.sh# NEW: manifest hygiene unit test (make test-scripts)
docs/04-container-runner-design.md# §1: parity + sudo notes
docs/05-security-and-isolation.md # new §7: in-container elevation model
docs/18-runner-image-package-parity.md # this document
README.md                         # Features entry at implementation; roadmap now
```

## 6. Alternatives Considered

1. **Full upstream-toolchain replication** — rejected: tens of GB, x64-leaning
   (Android/CodeQL), unmaintainable without their Packer pipeline; provides
   little value because `setup-*` actions fetch toolchains on demand.
2. **Runtime installer (entrypoint apt layer per container start)** — rejected:
   adds startup latency and network flakiness to every pool scale-up, makes
   images non-deterministic, and breaks offline clusters.
3. **Inline package list in the Dockerfile** — rejected: untestable, unreadable
   diff noise on every rev; the manifest + installer split gives us `make
   test-scripts` hooks and a future CI drift guard.
4. **Sudo-free parity (document "no sudo, pre-install everything")** — rejected:
   breaks the dominant community-workflow pattern (`sudo apt-get install`) and
   diverges from the hosted contract this milestone is about.

## 7. Resolved Decisions (Design Review)

1. **`ImageVersion` format:** confirmed — GitHub-hosted style `24.04.<yyyymm>`
   (build-arg default `24.04.$(date +%Y%m)` at image build time).
2. **Curated Tier-3 subset** (kubectl/helm etc.): rejected for now — wait for
   demand; passwordless sudo + `setup-*` actions cover it.
3. **`tk`:** kept — Python `tkinter` parity is worth the ~40 MB dep tree.
4. **`xvfb`:** re-added after initial implementation — some GUI-style workflow
   suites (canvas/screenshot tests via `xvfb-run`) need it on every runner, and
   per-job `sudo apt-get install` was deemed friction for a common-enough case.
   Manifest, drop-guard test, and README M22 updated accordingly (69 entries).

## 8. Implementation Notes (as-built)

Recorded for accuracy against the merged implementation on
`feature/runner-image-package-parity`:

- **Noble package-name mappings:** two upstream names have no installation
  candidate on Ubuntu 24.04 and are mapped in the manifest — virtual `netcat` →
  `netcat-openbsd` (the standard provider) and `upx` → `upx-ucl` (Debian/Ubuntu's
  UPX package name). The full manifest was validated against a live
  `ubuntu:24.04` apt index (amd64) in addition to the unit tests.
- **Tier 2 layer does not re-install sudo:** the §3.3 sketch's
  `apt-get install sudo` would force a redundant `apt-get update` (the parity
  layer already cleans lists). The parity manifest provides `sudo`; the Tier 2
  layer is therefore only `usermod -aG sudo runner`, the sudoers drop-in,
  `chmod 0440`, and `visudo -cf`.
- **Numeric `USER` and group visibility:** the image keeps `USER 1001:1001`
  (deterministic, override-friendly). Because the USER is numeric, Docker does
  not resolve supplementary groups at container start, so `id` reports only
  `groups=1001(runner)` even though `runner` is a member of `sudo` in
  `/etc/group`. Elevation is unaffected — the sudoers rule grants `runner`
  directly (verified: `sudo -n id` → `uid=0`).
- **`ImageVersion` plumbing:** `Dockerfile` declares
  `ARG IMAGE_VERSION=24.04.0` and `ENV ImageVersion=${IMAGE_VERSION}`;
  `make build-image-runner` supplies the GH-style stamp via
  `--build-arg IMAGE_VERSION=24.04.$$(date +%Y%m)` (Make expands the date at
  build time).
