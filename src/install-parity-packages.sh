#!/bin/bash
set -euo pipefail

# Install the GitHub ubuntu-24.04 hosted-runner parity apt packages listed in
# runner-parity-packages.txt (see docs/18-runner-image-package-parity.md).
#
# Usage: install-parity-packages.sh [manifest-path]
#
# Behaviour:
#   - skips packages already installed in the image (idempotent, keeps the
#     manifest a superset of the Dockerfile's hand-picked packages)
#   - installs everything else in a single apt-get transaction
#   - fails closed on unavailable/arch-missing packages (no silent arch skew)
#   - cleans apt lists in the same invocation (hadolint DL3009)

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MANIFEST="${1:-${SCRIPT_DIR}/runner-parity-packages.txt}"

if [ ! -r "${MANIFEST}" ]; then
	echo "ERROR: parity manifest not readable: ${MANIFEST}" >&2
	exit 1
fi

# Strip comments and blank lines into an array of package names.
mapfile -t PACKAGES < <(grep -vE '^[[:space:]]*(#|$)' "${MANIFEST}")

if [ "${#PACKAGES[@]}" -eq 0 ]; then
	echo "ERROR: parity manifest contains no packages: ${MANIFEST}" >&2
	exit 1
fi

# Validate package names early: fail the build on manifest typos rather than
# letting apt interpret garbage.
for pkg in "${PACKAGES[@]}"; do
	if [[ ! "${pkg}" =~ ^[a-z0-9][a-z0-9+.\-]*$ ]]; then
		echo "ERROR: invalid package name in manifest: '${pkg}'" >&2
		exit 1
	fi
done

# De-duplicate while preserving order.
mapfile -t UNIQUE < <(printf '%s\n' "${PACKAGES[@]}" | awk '!seen[$0]++')

# Skip packages already present (status "ii" = installed).
TO_INSTALL=()
for pkg in "${UNIQUE[@]}"; do
	status="$(dpkg-query -W -f='${db:Status-Abbrev}' "${pkg}" 2>/dev/null || true)"
	if [ "${status}" != "ii " ]; then
		TO_INSTALL+=("${pkg}")
	fi
done

if [ "${#TO_INSTALL[@]}" -eq 0 ]; then
	echo "All ${#UNIQUE[@]} parity packages already installed; nothing to do."
	exit 0
fi

echo "Installing ${#TO_INSTALL[@]}/${#UNIQUE[@]} parity packages from ${MANIFEST}"

export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends "${TO_INSTALL[@]}"
apt-get clean
rm -rf /var/lib/apt/lists/*
