#!/usr/bin/env bash
# Executor for Go toolchain bump. All policy and filtering live in
# scripts/check-go-bump-policy.sh.
#
# Usage: bump-go.sh <version>
#   <version> is the exact go directive (e.g. 1.26.2), no "go" prefix.

set -euo pipefail

if [[ $# -lt 1 || -z "${1}" ]]; then
  echo "usage: bump-go.sh <version>" >&2
  echo "  example: bump-go.sh 1.26.2" >&2
  exit 1
fi

version="${1#go}"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
GO_MOD="${ROOT_DIR}/go.mod"

printf 'bump-go: bumping Go version to %s\n' "${version}"

# TEST_BUMP_DRY_RUN=1 lets the test suite confirm the script is reached
# without touching real files (check-go-bump-policy.sh uses an absolute path
# to invoke this script so PATH-based stubbing cannot intercept it).
if [[ "${TEST_BUMP_DRY_RUN:-}" == "1" ]]; then
  printf 'bump-go: dry-run, skipping file updates\n'
  exit 0
fi

# 1. Update root go.mod go directive.
#    Use a temp-file swap for cross-platform compatibility (GNU vs BSD sed).
tmpfile=$(mktemp)
sed "s|^go [0-9][0-9.]*$|go ${version}|" "${GO_MOD}" > "${tmpfile}"
mv "${tmpfile}" "${GO_MOD}"
printf 'bump-go: updated %s\n' "${GO_MOD}"

# 2. Propagate the new version to all other tracked files (Dockerfiles,
#    .tool-versions, secondary go.mod files, pre-commit config, etc.).
#    Pass FULL_VERSION so update_go_version.sh skips `go list`, which would
#    require the exact new toolchain to be installed.
FULL_VERSION="${version}" "${SCRIPT_DIR}/dev/update_go_version.sh"
