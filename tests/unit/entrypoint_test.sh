#!/bin/bash
set -euo pipefail

# Unit test suite for src/entrypoint.sh provider-mode detection and registration flows.

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ENTRYPOINT="${REPO_ROOT}/src/entrypoint.sh"

TEST_TMP="$(mktemp -d)"
cleanup_test() {
	rm -rf "${TEST_TMP}"
}
trap cleanup_test EXIT

MOCK_BIN="${TEST_TMP}/bin"
mkdir -p "${MOCK_BIN}"

# Create mock act_runner
cat << 'EOF' > "${MOCK_BIN}/act_runner"
#!/usr/bin/env bash
echo "$@" >> "${MOCK_LOG_ACT_RUNNER:-/tmp/mock_act_runner.log}"
if [ "${1:-}" = "generate-config" ]; then
    echo "runner: dummy_gitea_config"
    exit 0
fi
if [ "${1:-}" = "daemon" ] || [ "${3:-}" = "daemon" ]; then
    exit 0
fi
exit 0
EOF
chmod +x "${MOCK_BIN}/act_runner"

# Create mock forgejo-runner
cat << 'EOF' > "${MOCK_BIN}/forgejo-runner"
#!/usr/bin/env bash
echo "$@" >> "${MOCK_LOG_FORGEJO_RUNNER:-/tmp/mock_forgejo_runner.log}"
if [ "${1:-}" = "generate-config" ]; then
    echo "runner: dummy_forgejo_config"
    exit 0
fi
if [ "${1:-}" = "daemon" ] || [ "${3:-}" = "daemon" ]; then
    exit 0
fi
exit 0
EOF
chmod +x "${MOCK_BIN}/forgejo-runner"

# Create mock config.sh and run.sh for GitHub mode
cat << 'EOF' > "${MOCK_BIN}/config.sh"
#!/usr/bin/env bash
echo "$@" >> "${MOCK_LOG_GH_CONFIG:-/tmp/mock_gh_config.log}"
exit 0
EOF
chmod +x "${MOCK_BIN}/config.sh"

cat << 'EOF' > "${MOCK_BIN}/run.sh"
#!/usr/bin/env bash
echo "run.sh executed" >> "${MOCK_LOG_GH_RUN:-/tmp/mock_gh_run.log}"
exit 0
EOF
chmod +x "${MOCK_BIN}/run.sh"

export PATH="${MOCK_BIN}:${PATH}"

echo "Running entrypoint unit tests..."

# ------------------------------------------------------------------------------
# Test 1: GitHub Mode Detection and Registration
# ------------------------------------------------------------------------------
echo -n "Test 1: GitHub provider mode... "
WORK_DIR="${TEST_TMP}/work_github"
mkdir -p "${WORK_DIR}"
cp "${MOCK_BIN}/config.sh" "${WORK_DIR}/config.sh"
cp "${MOCK_BIN}/run.sh" "${WORK_DIR}/run.sh"

export MOCK_LOG_GH_CONFIG="${TEST_TMP}/gh_config.log"
export MOCK_LOG_GH_RUN="${TEST_TMP}/gh_run.log"

(
	cd "${WORK_DIR}"
	env -i PATH="${PATH}" \
		MOCK_LOG_GH_CONFIG="${MOCK_LOG_GH_CONFIG}" \
		MOCK_LOG_GH_RUN="${MOCK_LOG_GH_RUN}" \
		GITHUB_REPOSITORY_URL="https://github.com/my-org/my-repo" \
		RUNNER_TOKEN="gh_token_123" \
		RUNNER_NAME="test-runnero" \
		RUNNER_LABELS="github-label" \
		bash "${ENTRYPOINT}" > /dev/null 2>&1
)

if grep -q -- "--url https://github.com/my-org/my-repo" "${MOCK_LOG_GH_CONFIG}" && \
   grep -q -- "--token gh_token_123" "${MOCK_LOG_GH_CONFIG}" && \
   grep -q -- "--ephemeral" "${MOCK_LOG_GH_CONFIG}"; then
	echo "PASSED"
else
	echo "FAILED: config.sh was not called with expected arguments"
	cat "${MOCK_LOG_GH_CONFIG}"
	exit 1
fi

# ------------------------------------------------------------------------------
# Test 2: Gitea Mode Detection and Registration
# ------------------------------------------------------------------------------
echo -n "Test 2: Gitea provider mode... "
WORK_DIR_GITEA="${TEST_TMP}/work_gitea"
mkdir -p "${WORK_DIR_GITEA}"
export MOCK_LOG_ACT_RUNNER="${TEST_TMP}/act_runner.log"

(
	cd "${WORK_DIR_GITEA}"
	env -i PATH="${PATH}" \
		MOCK_LOG_ACT_RUNNER="${MOCK_LOG_ACT_RUNNER}" \
		RUNNER_PROVIDER="gitea" \
		GITEA_INSTANCE_URL="https://gitea.example.com" \
		RUNNER_TOKEN="gitea_token_456" \
		RUNNER_NAME="test-gitea-runner" \
		bash "${ENTRYPOINT}" > /dev/null 2>&1
)

if grep -q -- "register" "${MOCK_LOG_ACT_RUNNER}" && \
   grep -q -- "--instance https://gitea.example.com" "${MOCK_LOG_ACT_RUNNER}" && \
   grep -q -- "--token gitea_token_456" "${MOCK_LOG_ACT_RUNNER}"; then
	echo "PASSED"
else
	echo "FAILED: act_runner was not called with expected arguments"
	cat "${MOCK_LOG_ACT_RUNNER}"
	exit 1
fi

# ------------------------------------------------------------------------------
# Test 3: Forgejo Mode Detection and Registration
# ------------------------------------------------------------------------------
echo -n "Test 3: Forgejo provider mode... "
WORK_DIR_FORGEJO="${TEST_TMP}/work_forgejo"
mkdir -p "${WORK_DIR_FORGEJO}"
export MOCK_LOG_FORGEJO_RUNNER="${TEST_TMP}/forgejo_runner.log"

(
	cd "${WORK_DIR_FORGEJO}"
	env -i PATH="${PATH}" \
		MOCK_LOG_FORGEJO_RUNNER="${MOCK_LOG_FORGEJO_RUNNER}" \
		FORGEJO_INSTANCE_URL="https://forgejo.example.com" \
		RUNNER_TOKEN="forgejo_token_789" \
		RUNNER_NAME="test-forgejo-runner" \
		bash "${ENTRYPOINT}" > /dev/null 2>&1
)

if grep -q -- "register" "${MOCK_LOG_FORGEJO_RUNNER}" && \
   grep -q -- "--instance https://forgejo.example.com" "${MOCK_LOG_FORGEJO_RUNNER}" && \
   grep -q -- "--token forgejo_token_789" "${MOCK_LOG_FORGEJO_RUNNER}"; then
	echo "PASSED"
else
	echo "FAILED: forgejo-runner was not called with expected arguments"
	cat "${MOCK_LOG_FORGEJO_RUNNER}"
	exit 1
fi

# ------------------------------------------------------------------------------
# Test 4: Missing RUNNER_TOKEN Exits with Error
# ------------------------------------------------------------------------------
echo -n "Test 4: Missing RUNNER_TOKEN fails... "
set +e
ERR_OUT=$(env -i PATH="${PATH}" GITHUB_REPOSITORY_URL="https://github.com/o/r" bash "${ENTRYPOINT}" 2>&1)
EXIT_CODE=$?
set -e

if [ "${EXIT_CODE}" -ne 0 ] && echo "${ERR_OUT}" | grep -q "RUNNER_TOKEN environment variable is not defined"; then
	echo "PASSED"
else
	echo "FAILED: expected non-zero exit and error message, got exit=${EXIT_CODE}, output=${ERR_OUT}"
	exit 1
fi

# ------------------------------------------------------------------------------
# Test 5: Missing URL Exits with Error
# ------------------------------------------------------------------------------
echo -n "Test 5: Missing Provider URL fails... "
set +e
ERR_OUT=$(env -i PATH="${PATH}" RUNNER_PROVIDER="gitea" RUNNER_TOKEN="tok" bash "${ENTRYPOINT}" 2>&1)
EXIT_CODE=$?
set -e

if [ "${EXIT_CODE}" -ne 0 ] && echo "${ERR_OUT}" | grep -q "GITEA_INSTANCE_URL"; then
	echo "PASSED"
else
	echo "FAILED: expected non-zero exit for missing Gitea URL, got exit=${EXIT_CODE}, output=${ERR_OUT}"
	exit 1
fi

# ------------------------------------------------------------------------------
# Test 6: GitHub Interrupt Trap Triggers De-Registration
# ------------------------------------------------------------------------------
echo -n "Test 6: GitHub interrupt trap triggers de-registration... "
WORK_DIR_GH_TRAP="${TEST_TMP}/work_github_trap"
mkdir -p "${WORK_DIR_GH_TRAP}"

cat << 'EOF' > "${WORK_DIR_GH_TRAP}/run.sh"
#!/usr/bin/env bash
trap 'exit 0' SIGTERM SIGINT
sleep 30 &
wait $!
EOF
chmod +x "${WORK_DIR_GH_TRAP}/run.sh"
cp "${MOCK_BIN}/config.sh" "${WORK_DIR_GH_TRAP}/config.sh"

LOG_GH_TRAP="${TEST_TMP}/gh_trap.log"

(
	cd "${WORK_DIR_GH_TRAP}" && \
	exec env -i PATH="${PATH}" \
		MOCK_LOG_GH_CONFIG="${LOG_GH_TRAP}" \
		GITHUB_REPOSITORY_URL="https://github.com/my-org/my-repo" \
		RUNNER_TOKEN="gh_trap_token" \
		RUNNER_NAME="trap-runnero" \
		bash "${ENTRYPOINT}" > /dev/null 2>&1
) &
ENTRYPOINT_PID=$!

for _ in {1..50}; do
	if grep -q -- "--url https://github.com/my-org/my-repo" "${LOG_GH_TRAP}" 2>/dev/null; then
		break
	fi
	sleep 0.05
done

kill -TERM "${ENTRYPOINT_PID}" 2>/dev/null || true
wait "${ENTRYPOINT_PID}" 2>/dev/null || true

if grep -q -- "remove --token gh_trap_token" "${LOG_GH_TRAP}"; then
	echo "PASSED"
else
	echo "FAILED: GitHub runner de-registration was not triggered on interrupt"
	cat "${LOG_GH_TRAP}" 2>/dev/null || true
	exit 1
fi

# ------------------------------------------------------------------------------
# Test 7: Gitea Interrupt Trap Triggers De-Registration
# ------------------------------------------------------------------------------
echo -n "Test 7: Gitea interrupt trap triggers de-registration... "
WORK_DIR_GITEA_TRAP="${TEST_TMP}/work_gitea_trap"
mkdir -p "${WORK_DIR_GITEA_TRAP}"
LOG_GITEA_TRAP="${TEST_TMP}/gitea_trap.log"

MOCK_BIN_GITEA_TRAP="${TEST_TMP}/bin_gitea_trap"
mkdir -p "${MOCK_BIN_GITEA_TRAP}"
cat << 'EOF' > "${MOCK_BIN_GITEA_TRAP}/act_runner"
#!/usr/bin/env bash
trap 'exit 0' SIGTERM SIGINT
echo "$@" >> "${MOCK_LOG_ACT_RUNNER:-/tmp/mock_act_runner.log}"
if [ "${1:-}" = "generate-config" ]; then
    echo "runner: dummy_config"
    exit 0
fi
if [ "${3:-}" = "daemon" ] || [ "${1:-}" = "daemon" ]; then
    sleep 30 &
    wait $!
    exit 0
fi
exit 0
EOF
chmod +x "${MOCK_BIN_GITEA_TRAP}/act_runner"

(
	cd "${WORK_DIR_GITEA_TRAP}" && \
	exec env -i PATH="${MOCK_BIN_GITEA_TRAP}:${PATH}" \
		MOCK_LOG_ACT_RUNNER="${LOG_GITEA_TRAP}" \
		RUNNER_PROVIDER="gitea" \
		GITEA_INSTANCE_URL="https://gitea.example.com" \
		RUNNER_TOKEN="gitea_trap_token" \
		RUNNER_NAME="trap-gitea-runner" \
		bash "${ENTRYPOINT}" > /dev/null 2>&1
) &
ENTRYPOINT_PID=$!

for _ in {1..50}; do
	if grep -q -- "register" "${LOG_GITEA_TRAP}" 2>/dev/null; then
		break
	fi
	sleep 0.05
done

kill -TERM "${ENTRYPOINT_PID}" 2>/dev/null || true
wait "${ENTRYPOINT_PID}" 2>/dev/null || true

if grep -q -- "unregister" "${LOG_GITEA_TRAP}"; then
	echo "PASSED"
else
	echo "FAILED: Gitea act_runner unregister was not triggered on interrupt"
	cat "${LOG_GITEA_TRAP}" 2>/dev/null || true
	exit 1
fi

# ------------------------------------------------------------------------------
# Test 8: Forgejo Interrupt Trap Triggers De-Registration
# ------------------------------------------------------------------------------
echo -n "Test 8: Forgejo interrupt trap triggers de-registration... "
WORK_DIR_FORGEJO_TRAP="${TEST_TMP}/work_forgejo_trap"
mkdir -p "${WORK_DIR_FORGEJO_TRAP}"
LOG_FORGEJO_TRAP="${TEST_TMP}/forgejo_trap.log"

MOCK_BIN_FORGEJO_TRAP="${TEST_TMP}/bin_forgejo_trap"
mkdir -p "${MOCK_BIN_FORGEJO_TRAP}"
cat << 'EOF' > "${MOCK_BIN_FORGEJO_TRAP}/forgejo-runner"
#!/usr/bin/env bash
trap 'exit 0' SIGTERM SIGINT
echo "$@" >> "${MOCK_LOG_FORGEJO_RUNNER:-/tmp/mock_forgejo_runner.log}"
if [ "${1:-}" = "generate-config" ]; then
    echo "runner: dummy_config"
    exit 0
fi
if [ "${3:-}" = "daemon" ] || [ "${1:-}" = "daemon" ]; then
    sleep 30 &
    wait $!
    exit 0
fi
exit 0
EOF
chmod +x "${MOCK_BIN_FORGEJO_TRAP}/forgejo-runner"

(
	cd "${WORK_DIR_FORGEJO_TRAP}" && \
	exec env -i PATH="${MOCK_BIN_FORGEJO_TRAP}:${PATH}" \
		MOCK_LOG_FORGEJO_RUNNER="${LOG_FORGEJO_TRAP}" \
		FORGEJO_INSTANCE_URL="https://forgejo.example.com" \
		RUNNER_TOKEN="forgejo_trap_token" \
		RUNNER_NAME="trap-forgejo-runner" \
		bash "${ENTRYPOINT}" > /dev/null 2>&1
) &
ENTRYPOINT_PID=$!

for _ in {1..50}; do
	if grep -q -- "register" "${LOG_FORGEJO_TRAP}" 2>/dev/null; then
		break
	fi
	sleep 0.05
done

kill -TERM "${ENTRYPOINT_PID}" 2>/dev/null || true
wait "${ENTRYPOINT_PID}" 2>/dev/null || true

if grep -q -- "unregister" "${LOG_FORGEJO_TRAP}"; then
	echo "PASSED"
else
	echo "FAILED: Forgejo runner unregister was not triggered on interrupt"
	cat "${LOG_FORGEJO_TRAP}" 2>/dev/null || true
	exit 1
fi

echo "All entrypoint unit tests passed successfully!"
