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

# devcontainer CLI fires postStartCommand on container create, but can
# short-circuit when reusing an existing container (e.g. after a partial
# teardown), and any manual `docker compose down && up` recovery path
# skips lifecycle hooks entirely. post-start.sh is idempotent (rm -f
# stale ssh-agent socket, restart screen, best-effort PATCH host kfp),
# so re-firing is safe and closes that gap.
echo "==> Re-firing postStartCommand (defensive)"
devcontainer exec --workspace-folder "${workspace}" \
  bash /workspace/.devcontainer/scripts/post-start.sh 2>&1 \
  | sed 's/^/    /' \
  || echo "    (post-start.sh hit a non-fatal error; continuing)"

echo
echo "Devcontainer ready. To attach a shell from the host:"
echo "  devcontainer exec --workspace-folder \"${workspace}\" bash"
echo
echo "To attach the existing tmux session inside the container:"
echo "  devcontainer exec --workspace-folder \"${workspace}\" tmux a -t mck"
