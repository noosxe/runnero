# Unified Container Runner Design

This document covers the design of the runner container image, which acts as the execution agent. The container is designed to be highly versatile, supporting GitHub Actions, Gitea Actions, and Forgejo Actions workflows.

## 1. Unified Runner Image Strategy

To avoid maintaining separate container images for GitHub, Gitea, and Forgejo, we employ a unified `Dockerfile` that packages binaries for all three environments.

### Multi-Arch Strategy
The image natively supports `amd64` and `arm64` via multi-stage builds. During the build process, the correct architecture of the Gitea `act_runner`, Forgejo `forgejo-runner`, and GitHub Actions runner binaries are downloaded based on `${TARGETARCH}`:

```dockerfile
# Example Downloader stage for Gitea act_runner / forgejo-runner
ARG ACT_RUNNER_VERSION=0.2.11
RUN set -ex; \
    if [ "${TARGETARCH}" = "amd64" ] || [ -z "${TARGETARCH}" ]; then \
        GITEA_ARCH="amd64"; \
    elif [ "${TARGETARCH}" = "arm64" ]; then \
        GITEA_ARCH="arm64"; \
    else \
        echo "ERROR: Unsupported Target Architecture: ${TARGETARCH}" >&2; exit 1; \
    fi; \
    curl -o /usr/local/bin/act_runner -L "https://gitea.com/gitea/act_runner/releases/download/v${ACT_RUNNER_VERSION}/act_runner-${ACT_RUNNER_VERSION}-linux-${GITEA_ARCH}"; \
    chmod +x /usr/local/bin/act_runner

# Similarly for forgejo-runner:
# curl -o /usr/local/bin/forgejo-runner -L "https://code.forgejo.org/forgejo/runner/releases/download/v${FORGEJO_RUNNER_VERSION}/forgejo-runner-${FORGEJO_RUNNER_VERSION}-linux-${GITEA_ARCH}"; \
# chmod +x /usr/local/bin/forgejo-runner
```


### Workflow Compatibility: Package Parity & Passwordless Sudo

Workflows written against GitHub-hosted `ubuntu-24.04` runners assume a large
standard tool surface (build tooling, `python`, `openssh-client`, compression
utilities, …) and **passwordless sudo** for the `runner` user. The image tracks
GitHub's `actions/runner-images` `toolset-2404.json` apt package set for parity,
with a small documented set of container-inappropriate exclusions (GUI/systemd
daemons such as `dbus`, `xvfb`, `haveged`), and grants `runner` `NOPASSWD:ALL`
sudo via a build-validated `/etc/sudoers.d` drop-in. Multi-gigabyte upstream
toolchains (language toolcaches, SDKs, browsers, DB servers) are deliberately
excluded — `actions/setup-*` steps install them on demand into
`RUNNER_TOOL_CACHE=/opt/hostedtoolcache`. Full tiering, the package manifest,
and the security analysis live in [docs/18](18-runner-image-package-parity.md).

## 2. Entrypoint Orchestration (`src/entrypoint.sh`)

At container runtime, the entrypoint script acts as an internal orchestrator, determining its mode (GitHub, Gitea, or Forgejo) based on injected environment variables.

```bash
#!/bin/bash
set -euo pipefail

# Determine provider mode
PROVIDER_MODE="github"
if [ -n "${FORGEJO_INSTANCE_URL:-}" ] || [ "${RUNNER_PROVIDER:-}" = "forgejo" ]; then
    PROVIDER_MODE="forgejo"
elif [ -n "${GITEA_INSTANCE_URL:-}" ] || [ "${RUNNER_PROVIDER:-}" = "gitea" ]; then
    PROVIDER_MODE="gitea"
fi

if [ "$PROVIDER_MODE" = "github" ]; then
    # Execute GitHub Actions runner registration and startup
    ./config.sh --url "${GITHUB_REPOSITORY_URL}" --token "${RUNNER_TOKEN}" --ephemeral ...
    ./run.sh
elif [ "$PROVIDER_MODE" = "forgejo" ]; then
    # Execute Forgejo runner registration and startup
    export FORGEJO_RUNNER_EPHEMERAL=1
    
    forgejo-runner generate-config > /tmp/forgejo_config.yaml
    
    forgejo-runner register \
        --no-interactive \
        --instance "${FORGEJO_INSTANCE_URL}" \
        --token "${RUNNER_TOKEN}" \
        --name "${RUNNER_NAME}" \
        --labels "${RUNNER_LABELS}"
        
    forgejo-runner --config /tmp/forgejo_config.yaml daemon
else
    # Execute Gitea act_runner registration and startup
    export GITEA_RUNNER_EPHEMERAL=1
    
    # Generate act_runner configuration
    act_runner generate-config > /tmp/act_config.yaml
    
    # Register the runner
    act_runner register \
        --no-interactive \
        --instance "${GITEA_INSTANCE_URL}" \
        --token "${RUNNER_TOKEN}" \
        --name "${RUNNER_NAME}" \
        --labels "${RUNNER_LABELS}"
        
    # Start the daemon
    act_runner --config /tmp/act_config.yaml daemon
fi
```

## 3. Graceful Deregistration within Container

While the supervisor handles host-level cleanups, the container itself ensures it de-registers from the Git provider if it receives an interrupt signal before completing a job.

Traps are configured in `src/entrypoint.sh` to execute the appropriate deregistration action:

- **For GitHub**: Runs `./config.sh remove --token "${RUNNER_TOKEN}"` to clear the runner from the repository's settings.
- **For Gitea**: `act_runner` cleans itself up automatically when running in ephemeral mode (`GITEA_RUNNER_EPHEMERAL=1`), or can be manually removed via `act_runner unregister`.
- **For Forgejo**: `forgejo-runner` behaves similarly in ephemeral mode (`FORGEJO_RUNNER_EPHEMERAL=1`) or with `forgejo-runner unregister`.
