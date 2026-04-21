#!/usr/bin/env bash
# Executor for Go toolchain bump (not implemented). All policy and filtering
# live in scripts/check-go-bump-policy.sh.
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

# TODO: Implement bump logic here in next PRs.
# will invoke leverage scripts/dev/update_go_version.sh
printf '%s\n' "bump-go: target go version: ${version}"
