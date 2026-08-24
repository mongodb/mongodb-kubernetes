#!/bin/bash
# Runs on the host before the devcontainer starts (devcontainer.json
# initializeCommand; also wt-ctl's initialize_hook phase). cwd = the TARGET
# worktree; this script's own location = the TOOLING checkout — they are
# different trees when wt-ctl drives a worktree that carries no devcontainer
# tooling. Renders the per-worktree devcontainer config into
# <worktree>/.generated/devcontainer/ (gitignored on master, so portable
# worktrees stay clean).

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
TOOLING_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
WORKSPACE_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
DEVC_DIR="${WORKSPACE_ROOT}/.generated/devcontainer"
export TOOLING_ROOT WORKSPACE_ROOT DEVC_DIR

mkdir -p "${DEVC_DIR}"

COMPOSE_OVERRIDE_FILE="${DEVC_DIR}/compose.generated.yml"
export COMPOSE_OVERRIDE_FILE

function create_if_not_exists() {
    # `|| touch` (not `&& touch`): under set -e a `[[ ! -f ]] && touch` returns
    # non-zero when the file already exists (idempotent re-run) and aborts.
    [[ -f "$1" ]] || touch "$1"
}
create_if_not_exists "${COMPOSE_OVERRIDE_FILE}"
create_if_not_exists "${DEVC_DIR}/compose.user.yml"

# Migrate a legacy .devcontainer/compose.user.yml (worktrees rendered before
# the .generated/devcontainer move) once, while the new file is still empty.
if [[ -s "${WORKSPACE_ROOT}/.devcontainer/compose.user.yml" && ! -s "${DEVC_DIR}/compose.user.yml" ]]; then
    cp "${WORKSPACE_ROOT}/.devcontainer/compose.user.yml" "${DEVC_DIR}/compose.user.yml"
    echo "migrated legacy .devcontainer/compose.user.yml -> ${DEVC_DIR}/compose.user.yml"
fi

# Ensure ~/.ssh/known_hosts exists as a FILE before the evg-host-proxy bind
# mount resolves — otherwise Docker materializes it as a root-owned directory.
mkdir -p "${HOME}/.ssh"
create_if_not_exists "${HOME}/.ssh/known_hosts"

python3 "${SCRIPT_DIR}/initialize.py"
