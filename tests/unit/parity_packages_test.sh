#!/usr/bin/env bash
set -euo pipefail

# Unit test suite for the GitHub-hosted-runner parity package manifest
# (src/runner-parity-packages.txt) and its installer
# (src/install-parity-packages.sh). Design: docs/18-runner-image-package-parity.md.

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
MANIFEST="${REPO_ROOT}/src/runner-parity-packages.txt"
INSTALLER="${REPO_ROOT}/src/install-parity-packages.sh"

fail_test() {
	echo "FAILED: $1"
	exit 1
}

# ----------------------------------------------------------------------------
# 1. Manifest hygiene
# ----------------------------------------------------------------------------

echo "Test: manifest exists and is readable"
if [ -r "${MANIFEST}" ]; then
	echo "PASSED"
else
	fail_test "manifest missing or unreadable: ${MANIFEST}"
fi

# Extract package entries (comments and blanks stripped).
mapfile -t ENTRIES < <(grep -vE '^[[:space:]]*(#|$)' "${MANIFEST}")

echo "Test: manifest contains the full upstream-derived package set (69 entries)"
if [ "${#ENTRIES[@]}" -eq 69 ]; then
	echo "PASSED"
else
	fail_test "expected 69 entries (68 toolset-2404 kept + zstd), got ${#ENTRIES[@]}"
fi

echo "Test: every entry is a valid apt package name"
INVALID="$(printf '%s\n' "${ENTRIES[@]}" | grep -vE '^[a-z0-9][a-z0-9+.-]*$' || true)"
if [ -z "${INVALID}" ]; then
	echo "PASSED"
else
	fail_test "invalid package names: ${INVALID}"
fi

echo "Test: no duplicate entries"
DUPES="$(printf '%s\n' "${ENTRIES[@]}" | sort | uniq -d || true)"
if [ -z "${DUPES}" ]; then
	echo "PASSED"
else
	fail_test "duplicate entries: ${DUPES}"
fi

echo "Test: entries are LC_ALL=C sorted within each section"
check_section_sorted() {
	if [ "${#SEC[@]}" -gt 1 ]; then
		if ! LC_ALL=C sort -c <<<"$(printf '%s\n' "${SEC[@]}")" 2>/dev/null; then
			fail_test "section entries not LC_ALL=C sorted"
		fi
	fi
}
SEC=()
while IFS= read -r line; do
	case "${line}" in
	'# ---'*)
		check_section_sorted
		SEC=()
		;;
	'#'*) ;;
	'') ;;
	*) SEC+=("${line}") ;;
	esac
done <"${MANIFEST}"
check_section_sorted
echo "PASSED"

echo "Test: dropped upstream packages are absent (docs/18 §3.2)"
for dropped in dbus fonts-noto-color-emoji haveged pollinate ssh systemd-coredump; do
	if printf '%s\n' "${ENTRIES[@]}" | grep -qx "${dropped}"; then
		fail_test "dropped package '${dropped}' present in manifest"
	fi
done
echo "PASSED"

echo "Test: key parity packages are present"
for required in gcc g++ pkg-config openssh-client python-is-python3 sudo zstd libicu-dev xvfb; do
	found=0
	for entry in "${ENTRIES[@]}"; do
		if [ "${entry}" = "${required}" ]; then
			found=1
			break
		fi
	done
	if [ "${found}" -ne 1 ]; then
		fail_test "required package '${required}' missing from manifest"
	fi
done
echo "PASSED"

# ----------------------------------------------------------------------------
# 2. Installer behaviour (mocked apt-get / dpkg-query / rm)
# ----------------------------------------------------------------------------

echo "Test: installer passes bash syntax check"
if bash -n "${INSTALLER}"; then
	echo "PASSED"
else
	fail_test "installer has bash syntax errors"
fi

TEST_TMP="$(mktemp -d)"
cleanup_test() {
	rm -rf "${TEST_TMP}"
}
trap cleanup_test EXIT

MOCK_BIN="${TEST_TMP}/bin"
mkdir -p "${MOCK_BIN}"
MOCK_INSTALLED="${TEST_TMP}/installed.txt"
MOCK_APT_LOG="${TEST_TMP}/apt.log"
MOCK_INSTALL_LOG="${TEST_TMP}/install-args.log"
: >"${MOCK_INSTALLED}"
: >"${MOCK_APT_LOG}"
: >"${MOCK_INSTALL_LOG}"
export MOCK_INSTALLED MOCK_APT_LOG MOCK_INSTALL_LOG

# Mock dpkg-query: prints "ii " when the package (last arg) is in the
# installed list, otherwise exits 100 like the real tool.
cat <<'EOF' >"${MOCK_BIN}/dpkg-query"
#!/usr/bin/env bash
pkg="${@: -1}"
if grep -qx "${pkg}" "${MOCK_INSTALLED}"; then
	printf 'ii '
	exit 0
fi
exit 100
EOF
chmod +x "${MOCK_BIN}/dpkg-query"

# Mock apt-get: logs update/clean to MOCK_APT_LOG; logs every package passed
# to "install" (one per line) to MOCK_INSTALL_LOG.
cat <<'EOF' >"${MOCK_BIN}/apt-get"
#!/usr/bin/env bash
case "${1}" in
update) echo "update" >> "${MOCK_APT_LOG}" ;;
install)
	shift
	while [[ "${1:-}" == -* ]]; do shift; done
	printf '%s\n' "$@" >> "${MOCK_INSTALL_LOG}"
	;;
clean) echo "clean" >> "${MOCK_APT_LOG}" ;;
esac
exit 0
EOF
chmod +x "${MOCK_BIN}/apt-get"

# Mock rm: the real binary must never touch the host filesystem from tests.
cat <<'EOF' >"${MOCK_BIN}/rm"
#!/usr/bin/env bash
exit 0
EOF
chmod +x "${MOCK_BIN}/rm"

run_installer() {
	PATH="${MOCK_BIN}:${PATH}" bash "${INSTALLER}" "${MANIFEST}" \
		>"${TEST_TMP}/run.out" 2>"${TEST_TMP}/run.err"
}

echo "Test: installer runs update/clean around a single install transaction"
printf 'curl\njq\ngcc\n' >"${MOCK_INSTALLED}"
: >"${MOCK_APT_LOG}"
: >"${MOCK_INSTALL_LOG}"
run_installer
if grep -qx 'update' "${MOCK_APT_LOG}" && grep -qx 'clean' "${MOCK_APT_LOG}"; then
	echo "PASSED"
else
	fail_test "apt-get update/clean not both invoked (log: $(tr '\n' ' ' <"${MOCK_APT_LOG}"))"
fi

echo "Test: installer passes exactly the not-yet-installed set to apt-get"
# Expected = full manifest minus the 3 mocked-installed packages.
grep -vxE 'curl|jq|gcc' <(printf '%s\n' "${ENTRIES[@]}") | LC_ALL=C sort >"${TEST_TMP}/expected.txt"
LC_ALL=C sort "${MOCK_INSTALL_LOG}" >"${TEST_TMP}/actual.txt"
if diff -u "${TEST_TMP}/expected.txt" "${TEST_TMP}/actual.txt" >"${TEST_TMP}/diff.txt"; then
	echo "PASSED (65 packages in one transaction)"
else
	fail_test "install set mismatch: $(head -20 "${TEST_TMP}/diff.txt")"
fi

echo "Test: installer is idempotent when everything is installed"
printf '%s\n' "${ENTRIES[@]}" >"${MOCK_INSTALLED}"
: >"${MOCK_APT_LOG}"
: >"${MOCK_INSTALL_LOG}"
run_installer
if grep -q "nothing to do" "${TEST_TMP}/run.out" && [ ! -s "${MOCK_INSTALL_LOG}" ]; then
	echo "PASSED"
else
	fail_test "expected no-op run, got out='$(cat "${TEST_TMP}/run.out")'"
fi

echo "Test: installer fails closed on invalid manifest entries"
cp "${MANIFEST}" "${TEST_TMP}/bad.txt"
printf 'Not_A_Valid_Package!\n' >>"${TEST_TMP}/bad.txt"
: >"${MOCK_APT_LOG}"
: >"${MOCK_INSTALL_LOG}"
set +e
PATH="${MOCK_BIN}:${PATH}" bash "${INSTALLER}" "${TEST_TMP}/bad.txt" \
	>/dev/null 2>"${TEST_TMP}/bad.err"
EXIT_CODE=$?
set -e
if [ "${EXIT_CODE}" -ne 0 ] && [ ! -s "${MOCK_INSTALL_LOG}" ]; then
	echo "PASSED"
else
	fail_test "expected non-zero exit without apt invocation, got exit=${EXIT_CODE}"
fi

echo "All parity package unit tests passed successfully!"
