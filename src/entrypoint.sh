#!/bin/bash
set -euo pipefail

# Ensure Go module cache is writable if it exists, preventing tar extraction permission errors from previous runs
if [ -d "/home/runner/go" ]; then
	echo "Ensuring Go module cache at /home/runner/go is writable..."
	chmod -R +w /home/runner/go 2>/dev/null || true
fi

# ------------------------------------------------------------------------------
# Docker socket group access (docs/18 §3.7, RUN-162)
# ------------------------------------------------------------------------------
# Pools with docker access bind-mount the host docker.sock into the container
# (internal/orchestrator/docker). The socket's owning GID is host-specific and
# can never match a static group baked into the image, so the runner user would
# need sudo for every docker call. Instead, detect the mounted socket's GID at
# container start, grant the runner user membership of a matching group, and
# re-exec this entrypoint so the supplementary group is effective before the
# runner agent — and with it every job shell — starts. The socket inode itself
# is never chmod'ed: the host owns it, containers must not mutate host state.
#
# Group membership is only evaluated when a process is created, and a non-root
# process cannot join new groups — so the fix runs as a two-stage re-exec:
#
#   1. entrypoint (image user): resolve/create the group, then re-enter
#      through sudo as root. (`sudo -u runner` would be a credential no-op:
#      sudo skips re-initialising groups when the target uid equals ours.)
#   2. root stage: apply the membership, then drop back to the runner user
#      via setpriv --init-groups, which builds the full group vector from
#      /etc/group. RUNNER_SOCK_GROUPS_SYNCED guards against re-entry.
#
# The runner configuration environment survives the sudo re-entry via the
# sudoers env_keep whitelist (docs/18 §3.7).
DOCKER_SOCK="${RUNNER_DOCKER_SOCK:-/var/run/docker.sock}"
if [ -S "${DOCKER_SOCK}" ] && command -v sudo >/dev/null 2>&1 && [ "${RUNNER_SOCK_GROUPS_SYNCED:-}" != "1" ]; then
	SOCK_GID="$(stat -c '%g' "${DOCKER_SOCK}")"
	SOCK_GROUP="$(getent group "${SOCK_GID}" 2>/dev/null | cut -d: -f1 || true)"
	if [ -z "${SOCK_GROUP}" ]; then
		sudo -n groupadd -g "${SOCK_GID}" docker-sock 2>/dev/null ||
			sudo -n groupmod -g "${SOCK_GID}" docker-sock 2>/dev/null || true
		SOCK_GROUP="$(getent group "${SOCK_GID}" 2>/dev/null | cut -d: -f1 || true)"
	fi
	if [ "$(id -u)" = "0" ]; then
		# Root stage: apply the membership and drop privileges with a freshly
		# initialised group vector. Root never proceeds to the runner itself.
		if [ -n "${SOCK_GROUP}" ]; then
			usermod -aG "${SOCK_GROUP}" runner 2>/dev/null || true
		else
			echo "WARNING: could not resolve a group for docker.sock GID ${SOCK_GID}; docker jobs on this pool will need sudo." >&2
		fi
		export RUNNER_SOCK_GROUPS_SYNCED=1
		# The sudo re-entry rewrote identity env to root's (HOME=/root, USER=root,
		# ...) and setpriv preserves the environment as-is — without restoring the
		# runner user's identity here, the agent and every job shell would inherit
		# root's HOME (seen in CI as checkout failing on /root/.gitconfig).
		R_PASSWD="$(getent passwd runner 2>/dev/null || true)"
		R_HOME="$(echo "${R_PASSWD}" | cut -d: -f6)"
		R_SHELL="$(echo "${R_PASSWD}" | cut -d: -f7)"
		export HOME="${R_HOME:-/home/runner}" USER="runner" LOGNAME="runner" SHELL="${R_SHELL:-/bin/bash}"
		exec setpriv --reuid=1001 --regid=1001 --init-groups -- "$0"
	fi
	if [ -n "${SOCK_GROUP}" ]; then
		echo "Granting runner user docker.sock access via group '${SOCK_GROUP}' (GID ${SOCK_GID})..."
		# Re-enter as root to apply the membership (stage 2 above).
		exec sudo -n -- "$0"
	fi
	echo "WARNING: could not resolve a group for docker.sock GID ${SOCK_GID}; docker jobs on this pool will need sudo." >&2
fi

# Determine provider mode from environment variables (docs/04 §2)
PROVIDER_MODE="github"
if [ -n "${FORGEJO_INSTANCE_URL:-}" ] || [ "${RUNNER_PROVIDER:-}" = "forgejo" ]; then
	PROVIDER_MODE="forgejo"
elif [ -n "${GITEA_INSTANCE_URL:-}" ] || [ "${RUNNER_PROVIDER:-}" = "gitea" ]; then
	PROVIDER_MODE="gitea"
elif [ -n "${GITHUB_REPOSITORY_URL:-}" ] || [ "${RUNNER_PROVIDER:-}" = "github" ]; then
	PROVIDER_MODE="github"
fi

# Ensure registration token is provided
if [ -z "${RUNNER_TOKEN:-}" ]; then
	echo "ERROR: RUNNER_TOKEN environment variable is not defined." >&2
	exit 1
fi

# Default configuration settings
RUNNER_NAME="${RUNNER_NAME:-$(hostname)}"
RUNNER_WORKDIR="${RUNNER_WORKDIR:-_work}"
RUNNER_LABELS="${RUNNER_LABELS:-self-hosted,linux}"

CONFIG_PATH=""
RUNNER_PID=""

# Graceful cleanup and deregistration handler (docs/04 §3)
cleanup() {
	echo ""
	echo "======================================================="
	echo "Shutting down ${PROVIDER_MODE} runner gracefully..."
	echo "======================================================="
	if [ -n "${RUNNER_PID:-}" ]; then
		kill -TERM "${RUNNER_PID}" 2>/dev/null || true
		wait "${RUNNER_PID}" 2>/dev/null || true
	fi
	if [ "${PROVIDER_MODE}" = "github" ]; then
		if [ -f "./config.sh" ]; then
			echo "De-registering GitHub Actions runner..."
			./config.sh remove --token "${RUNNER_TOKEN}" 2>/dev/null || true
		fi
	elif [ "${PROVIDER_MODE}" = "gitea" ]; then
		echo "De-registering Gitea act_runner..."
		if [ -n "${CONFIG_PATH:-}" ] && [ -f "${CONFIG_PATH}" ]; then
			act_runner unregister --config "${CONFIG_PATH}" 2>/dev/null || true
		fi
	elif [ "${PROVIDER_MODE}" = "forgejo" ]; then
		echo "De-registering Forgejo runner..."
		if [ -n "${CONFIG_PATH:-}" ] && [ -f "${CONFIG_PATH}" ]; then
			forgejo-runner unregister --config "${CONFIG_PATH}" 2>/dev/null || true
		fi
	fi
	echo "Runner de-registration complete."
	exit 0
}

# Trap termination signals
trap 'cleanup' SIGINT SIGTERM

echo "======================================================="
echo "Initializing Self-Hosted Runner (${PROVIDER_MODE} mode)"
echo "  Runner Name : ${RUNNER_NAME}"
echo "  Labels      : ${RUNNER_LABELS}"
echo "  Work Dir    : ${RUNNER_WORKDIR}"

if [ "${PROVIDER_MODE}" = "github" ]; then
	GITHUB_URL="${GITHUB_REPOSITORY_URL:-${RUNNER_INSTANCE_URL:-}}"
	if [ -z "${GITHUB_URL}" ]; then
		echo "ERROR: GITHUB_REPOSITORY_URL (or RUNNER_INSTANCE_URL) is not defined." >&2
		exit 1
	fi
	echo "  Target URL  : ${GITHUB_URL}"
	echo "======================================================="

	if [ -d "/actions-runner" ]; then
		cd /actions-runner
	fi

	echo "Configuring GitHub Actions runner..."
	./config.sh \
		--url "${GITHUB_URL}" \
		--token "${RUNNER_TOKEN}" \
		--name "${RUNNER_NAME}" \
		--work "${RUNNER_WORKDIR}" \
		--labels "${RUNNER_LABELS}" \
		--unattended \
		--replace \
		--ephemeral

	echo "Starting GitHub Actions runner agent..."
	./run.sh &
	RUNNER_PID=$!
	wait "${RUNNER_PID}"

elif [ "${PROVIDER_MODE}" = "gitea" ]; then
	GITEA_URL="${GITEA_INSTANCE_URL:-${RUNNER_INSTANCE_URL:-}}"
	if [ -z "${GITEA_URL}" ]; then
		echo "ERROR: GITEA_INSTANCE_URL (or RUNNER_INSTANCE_URL) is not defined." >&2
		exit 1
	fi
	echo "  Target URL  : ${GITEA_URL}"
	echo "======================================================="

	export GITEA_RUNNER_EPHEMERAL=1
	mkdir -p "${RUNNER_WORKDIR}"
	CONFIG_PATH="${RUNNER_WORKDIR}/act_config.yaml"

	echo "Generating Gitea act_runner configuration..."
	act_runner generate-config >"${CONFIG_PATH}"

	echo "Registering Gitea act_runner..."
	act_runner register \
		--no-interactive \
		--instance "${GITEA_URL}" \
		--token "${RUNNER_TOKEN}" \
		--name "${RUNNER_NAME}" \
		--labels "${RUNNER_LABELS}" \
		--config "${CONFIG_PATH}"

	echo "Starting Gitea act_runner daemon..."
	act_runner --config "${CONFIG_PATH}" daemon &
	RUNNER_PID=$!
	wait "${RUNNER_PID}"

elif [ "${PROVIDER_MODE}" = "forgejo" ]; then
	FORGEJO_URL="${FORGEJO_INSTANCE_URL:-${RUNNER_INSTANCE_URL:-}}"
	if [ -z "${FORGEJO_URL}" ]; then
		echo "ERROR: FORGEJO_INSTANCE_URL (or RUNNER_INSTANCE_URL) is not defined." >&2
		exit 1
	fi
	echo "  Target URL  : ${FORGEJO_URL}"
	echo "======================================================="

	export FORGEJO_RUNNER_EPHEMERAL=1
	mkdir -p "${RUNNER_WORKDIR}"
	CONFIG_PATH="${RUNNER_WORKDIR}/forgejo_config.yaml"

	echo "Generating Forgejo runner configuration..."
	forgejo-runner generate-config >"${CONFIG_PATH}"

	echo "Registering Forgejo runner..."
	forgejo-runner register \
		--no-interactive \
		--instance "${FORGEJO_URL}" \
		--token "${RUNNER_TOKEN}" \
		--name "${RUNNER_NAME}" \
		--labels "${RUNNER_LABELS}" \
		--config "${CONFIG_PATH}"

	echo "Starting Forgejo runner daemon..."
	forgejo-runner --config "${CONFIG_PATH}" daemon &
	RUNNER_PID=$!
	wait "${RUNNER_PID}"

else
	echo "ERROR: Unknown runner provider mode: ${PROVIDER_MODE}" >&2
	exit 1
fi
