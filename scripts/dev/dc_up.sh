#!/usr/bin/env bash
#
# Boot the devcontainer (and its compose stack) in detached mode.
#
# Use this after dc_build.sh + evg_prepare.sh. It runs
# `devcontainer up` for the worktree and then prints the command for
# attaching a shell.
#
# Usage: dc_up.sh [--workspace-folder DIR] [extra-args...]
#
# DEPRECATED: prefer `wt-ctl up`. Shim preserved through the Phase-1/2/3
# transition so existing skills + scripts continue to work.
#
set -Eeou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
echo "[deprecated] dc_up.sh — use 'wt-ctl up' instead" >&2
exec "${script_dir}/wt-ctl" up "$@"
