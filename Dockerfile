# syntax=docker/dockerfile:1

# ------------------------------------------------------------------------------
# Stage 1: Downloader
# Download and verify actions-runner, act_runner, and forgejo-runner binaries
# ------------------------------------------------------------------------------
FROM ubuntu:24.04 AS downloader

# Prevent interactive prompts during apt package installation
ENV DEBIAN_FRONTEND=noninteractive

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

# hadolint ignore=DL3008
RUN apt-get update && apt-get install -y --no-install-recommends \
    curl \
    ca-certificates \
    tar \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

ARG TARGETARCH
ARG GH_RUNNER_VERSION=2.337.0
ARG ACT_RUNNER_VERSION=0.2.11
ARG FORGEJO_RUNNER_VERSION=6.2.2

WORKDIR /build

# Download, verify checksums, and unpack runner binaries for target architecture
RUN set -ex; \
    if [ "${TARGETARCH}" = "amd64" ] || [ -z "${TARGETARCH}" ]; then \
        GH_ARCH="x64"; \
        GH_SHA256="70920811a4f8ad4328818682bca5c6469c1c942fab52448868071d0063816613"; \
        GITEA_ARCH="amd64"; \
        GITEA_SHA256="7a5e833793286bbfd9b59ce682bd41fc3f1c096bae1bb2a09b66ab2f6dacf90c"; \
        FORGEJO_ARCH="amd64"; \
        FORGEJO_SHA256="b35e79d1bbe71df51eaef2e5b436572c77258bc5d271992fd786f6148294637f"; \
    elif [ "${TARGETARCH}" = "arm64" ]; then \
        GH_ARCH="arm64"; \
        GH_SHA256="9b1dc70626422526e3c94767cf024896beb15da5342a3f4819bf2feac13e0393"; \
        GITEA_ARCH="arm64"; \
        GITEA_SHA256="32207183f1fb6f28d0df1532fe326c2e4571a5fbd3d088f72f5e5593ae86bfc2"; \
        FORGEJO_ARCH="arm64"; \
        FORGEJO_SHA256="c6f6738b57be1608a645d3a201cf37840703f56dcda43abc5d802564c47d8bb4"; \
    else \
        echo "ERROR: Unsupported Target Architecture: ${TARGETARCH}" >&2; exit 1; \
    fi; \
    mkdir -p /build/actions-runner /build/bin; \
    # 1. GitHub Actions Runner \
    curl -o actions-runner.tar.gz -L "https://github.com/actions/runner/releases/download/v${GH_RUNNER_VERSION}/actions-runner-linux-${GH_ARCH}-${GH_RUNNER_VERSION}.tar.gz"; \
    echo "${GH_SHA256}  actions-runner.tar.gz" | sha256sum -c -; \
    tar xzf actions-runner.tar.gz -C /build/actions-runner; \
    rm actions-runner.tar.gz; \
    # 2. Gitea act_runner \
    curl -o /build/bin/act_runner -L "https://gitea.com/gitea/runner/releases/download/v${ACT_RUNNER_VERSION}/act_runner-${ACT_RUNNER_VERSION}-linux-${GITEA_ARCH}"; \
    echo "${GITEA_SHA256}  /build/bin/act_runner" | sha256sum -c -; \
    chmod +x /build/bin/act_runner; \
    # 3. Forgejo forgejo-runner \
    curl -o /build/bin/forgejo-runner -L "https://code.forgejo.org/forgejo/runner/releases/download/v${FORGEJO_RUNNER_VERSION}/forgejo-runner-${FORGEJO_RUNNER_VERSION}-linux-${FORGEJO_ARCH}"; \
    echo "${FORGEJO_SHA256}  /build/bin/forgejo-runner" | sha256sum -c -; \
    chmod +x /build/bin/forgejo-runner

# ------------------------------------------------------------------------------
# Stage 2: Final Runtime Image
# ------------------------------------------------------------------------------
FROM ubuntu:24.04

# Prevent interactive prompts during apt package configuration
ENV DEBIAN_FRONTEND=noninteractive

# Configure Go to leave the module cache writable, preventing cache permission issues
ENV GOFLAGS="-modcacherw"

# GitHub-hosted-runner parity environment (docs/18 §3.4): ImageOS/ImageVersion
# identify the image to actions, RUNNER_TOOL_CACHE gives setup-* actions the
# hosted-standard on-demand toolchain location.
ENV ImageOS=ubuntu24
ENV RUNNER_TOOL_CACHE=/opt/hostedtoolcache
ARG IMAGE_VERSION=24.04.0
ENV ImageVersion=${IMAGE_VERSION}

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

# Install essential CLI tools required for runner setup, basic workflows, and Docker repository setup
# hadolint ignore=DL3008
RUN apt-get update && apt-get install -y --no-install-recommends \
    curl \
    tar \
    ca-certificates \
    git \
    git-lfs \
    jq \
    iproute2 \
    libatomic1 \
    make \
    unzip \
    zip \
    wget \
    gnupg \
    lsb-release \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

# Install official Docker CLI, Buildx, and Compose plugins
# hadolint ignore=DL3008
RUN mkdir -p /etc/apt/keyrings \
    && curl -fsSL https://download.docker.com/linux/ubuntu/gpg | gpg --dearmor -o /etc/apt/keyrings/docker.gpg \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu $(lsb_release -cs) stable" | tee /etc/apt/sources.list.d/docker.list > /dev/null \
    && apt-get update && apt-get install -y --no-install-recommends \
    docker-ce-cli \
    docker-buildx-plugin \
    docker-compose-plugin \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

# Install GitHub ubuntu-24.04 hosted-runner parity apt packages (docs/18 Tier 1).
# The manifest-driven installer skips already-present packages and performs a
# single apt transaction with list cleanup in this same layer.
# hadolint ignore=DL3008 DL3009
COPY src/runner-parity-packages.txt src/install-parity-packages.sh /tmp/parity/
RUN /tmp/parity/install-parity-packages.sh /tmp/parity/runner-parity-packages.txt \
    && rm -rf /tmp/parity

# Establish a dedicated non-root user and group with UID 1001 and GID 1001 (Least Privilege)
RUN groupadd -g 1001 runner \
    && useradd -m -u 1001 -g 1001 -s /bin/bash runner \
    && mkdir -p /home/runner/.cache /home/runner/.config \
    && chown -R 1001:1001 /home/runner

# Grant the runner user passwordless sudo, matching GitHub-hosted runners
# (docs/18 Tier 2 / §3.3). Sudo itself is installed by the parity manifest;
# visudo validates the drop-in so a malformed file can never ship.
RUN usermod -aG sudo runner \
    && echo 'runner ALL=(ALL:ALL) NOPASSWD:ALL' > /etc/sudoers.d/90-runner \
    && chmod 0440 /etc/sudoers.d/90-runner \
    && visudo -cf /etc/sudoers.d/90-runner > /dev/null

# Hosted-standard on-demand toolchain cache for actions/setup-* (docs/18 Tier 4)
RUN mkdir -p /opt/hostedtoolcache \
    && chown -R 1001:1001 /opt/hostedtoolcache

# Copy verified binaries and runner distribution from downloader stage (read-only for non-root runner)
COPY --from=downloader --chown=root:root --chmod=755 /build/bin/act_runner /usr/local/bin/act_runner
COPY --from=downloader --chown=root:root --chmod=755 /build/bin/forgejo-runner /usr/local/bin/forgejo-runner
COPY --from=downloader /build/actions-runner /actions-runner

# Setup workspace and install agent runtime dependencies
WORKDIR /actions-runner
RUN ./bin/installdependencies.sh \
    && mkdir -p /actions-runner/_work \
    && chown -R 1001:1001 /actions-runner \
    && chmod -R 755 /actions-runner

# Copy and secure the orchestration entrypoint script (executable by runner, owned by root)
COPY --chown=root:root --chmod=755 src/entrypoint.sh /entrypoint.sh

# Switch execution context to the dedicated non-root user (UID 1001, GID 1001)
USER 1001:1001

# Run the orchestration script
ENTRYPOINT ["/entrypoint.sh"]
