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
# DEPRECATED: prefer `wt-ctl build`. Shim preserved through the Phase-1/2/3
# transition so existing skills + scripts continue to work.
#
set -Eeou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
echo "[deprecated] dc_build.sh — use 'wt-ctl build' instead" >&2
exec "${script_dir}/wt-ctl" build "$@"
