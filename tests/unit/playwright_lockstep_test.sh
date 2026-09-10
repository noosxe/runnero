#!/usr/bin/env bash
set -euo pipefail

# Unit tests for the E2E Playwright image dependency hygiene (RUN-130):
#
#   1. tests/e2e/pnpm-lock.yaml exists and is committed.
#   2. The lockfile records @playwright/test at the version package.json asks
#      for (lockfile in sync with the manifest).
#   3. The Dockerfile.playwright base image tag equals the @playwright/test
#      version: the mcr.microsoft.com/playwright:vX.Y.Z image ships the exact
#      browser builds release X.Y.Z expects; a mismatch fails every test at
#      browser launch.
#   4. The Dockerfile installs with --frozen-lockfile so image builds cannot
#      silently resolve newer versions than the lockfile recorded.

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PKG_JSON="${REPO_ROOT}/tests/e2e/package.json"
LOCKFILE="${REPO_ROOT}/tests/e2e/pnpm-lock.yaml"
DOCKERFILE="${REPO_ROOT}/tests/e2e/Dockerfile.playwright"

fail_test() {
	echo "FAILED: $1"
	exit 1
}

echo "Test: pnpm lockfile is committed (tests/e2e/pnpm-lock.yaml)"
if [ -r "${LOCKFILE}" ]; then
	echo "PASSED"
else
	fail_test "lockfile missing or unreadable: ${LOCKFILE}"
fi

# The exact @playwright/test spec from package.json devDependencies.
echo "Test: @playwright/test is pinned to an exact version in package.json"
PW_SPEC="$(sed -n 's/.*"@playwright\/test":[[:space:]]*"\([^"]*\)".*/\1/p' "${PKG_JSON}")"
case "${PW_SPEC}" in
[0-9]*.[0-9]*.[0-9]*) echo "PASSED (exact pin: ${PW_SPEC})" ;;
*) fail_test "@playwright/test missing or not pinned exactly in ${PKG_JSON} (got '${PW_SPEC}', expected X.Y.Z)" ;;
esac

echo "Test: lockfile records @playwright/test at the pinned version"
if grep -q "'@playwright/test@${PW_SPEC}'" "${LOCKFILE}"; then
	echo "PASSED"
else
	fail_test "pnpm-lock.yaml does not contain '@playwright/test@${PW_SPEC}' — run 'pnpm install --lockfile-only' to resync"
fi

echo "Test: Dockerfile base image tag matches @playwright/test version"
BASE_TAG="$(sed -n 's/^FROM mcr\.microsoft\.com\/playwright:v\([0-9]*\.[0-9]*\.[0-9]*\).*/\1/p' "${DOCKERFILE}" | head -1)"
if [ -z "${BASE_TAG}" ]; then
	fail_test "no mcr.microsoft.com/playwright:vX.Y.Z base image found in ${DOCKERFILE}"
fi
if [ "${BASE_TAG}" = "${PW_SPEC}" ]; then
	echo "PASSED (v${BASE_TAG})"
else
	fail_test "base image is v${BASE_TAG} but @playwright/test is ${PW_SPEC} — keep them in lockstep (browser revision mismatch breaks browser launch)"
fi

echo "Test: Dockerfile installs with --frozen-lockfile"
if grep -qE '^RUN pnpm install --frozen-lockfile' "${DOCKERFILE}"; then
	echo "PASSED"
else
	fail_test "Dockerfile.playwright must install with 'pnpm install --frozen-lockfile' (RUN-130)"
fi

echo "All playwright lockstep tests passed."
