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

# Reconcile sidecar services with compose.user.yml overrides. devcontainer
# CLI's `up` uses --no-recreate, so when compose.user.yml changes a
# sidecar's image (e.g. pinning k8s-proxy to a locally-built tag instead
# of the canonical ghcr image) the running container keeps the OLD image
# and the override silently has no effect. Detect every service named in
# compose.user.yml and force-recreate just those (with --no-deps, so the
# devcontainer service itself isn't disturbed).
user_compose="${workspace}/.devcontainer/compose.user.yml"
if [[ -s "${user_compose}" ]] && command -v yq >/dev/null 2>&1; then
  overridden=()
  while IFS= read -r svc; do
    [[ -n "${svc}" && "${svc}" != "null" ]] && overridden+=("${svc}")
  done < <(yq -r '.services // {} | keys | .[]?' "${user_compose}" 2>/dev/null)
  if (( ${#overridden[@]} > 0 )); then
    proj="$(basename "${workspace}" | tr '[:upper:]' '[:lower:]')_devcontainer"
    echo "==> Reconciling compose.user.yml overrides: ${overridden[*]}"
    docker compose -p "${proj}" \
      -f "${workspace}/.devcontainer/compose.yml" \
      -f "${workspace}/.devcontainer/compose.generated.yml" \
      -f "${user_compose}" \
      up -d --force-recreate --no-deps "${overridden[@]}" 2>&1 \
      | sed 's/^/    /' \
      || echo "    (override reconcile hit a non-fatal error; continuing)"
  fi
fi

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
