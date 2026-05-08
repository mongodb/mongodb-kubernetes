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

echo "==> devcontainer up (workspace=${workspace})"
devcontainer up --workspace-folder "${workspace}" "${extra_args[@]}"

echo
echo "Devcontainer ready. To attach a shell from the host:"
echo "  devcontainer exec --workspace-folder \"${workspace}\" bash"
echo
echo "To attach the existing tmux session inside the container:"
echo "  devcontainer exec --workspace-folder \"${workspace}\" tmux a -t mck"
