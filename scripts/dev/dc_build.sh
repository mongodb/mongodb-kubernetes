#!/usr/bin/env bash
#
# Build the devcontainer image for the current worktree.
#
# Runs `devcontainer build` for this worktree. Exists as a separate step so
# that orchestrator scripts can run it in parallel with evergreen-host
# preparation.
#
# Usage: dc_build.sh [--workspace-folder DIR] [extra-args...]
#

set -Eeou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

workspace="$(pwd)"
extra_args=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --workspace-folder) workspace="$2"; shift 2 ;;
    -h|--help) echo "Usage: $0 [--workspace-folder DIR] [extra-args...]"; exit 0 ;;
    *) extra_args+=("$1"); shift ;;
  esac
done

if ! command -v devcontainer >/dev/null 2>&1; then
  echo "ERROR: devcontainer CLI not found in PATH." >&2
  exit 1
fi

echo "==> devcontainer build (workspace=${workspace})"
devcontainer build --workspace-folder "${workspace}" "${extra_args[@]}"
