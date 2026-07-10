#!/bin/bash
# Runs on the host before the devcontainer starts (devcontainer.json initializeCommand).

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
export ENV_DIR="${SCRIPT_DIR}/../.envs"
mkdir -p "${ENV_DIR}"

# Ensure the project-level .generated/ exists so the evg-host-proxy bind mount
# resolves cleanly even before the first switch_context run.
mkdir -p "${SCRIPT_DIR}/../../.generated"

COMPOSE_OVERRIDE_FILE="${SCRIPT_DIR}/../compose.generated.yml"
export COMPOSE_OVERRIDE_FILE

function create_if_not_exists() {
    # `|| touch` (not `&& touch`): under set -e a `[[ ! -f ]] && touch` returns
    # non-zero when the file already exists (idempotent re-run) and aborts.
    [[ -f "$1" ]] || touch "$1"
}
create_if_not_exists "${COMPOSE_OVERRIDE_FILE}"
create_if_not_exists "${SCRIPT_DIR}/../compose.user.yml"

# Ensure ~/.ssh/known_hosts exists as a FILE before the evg-host-proxy bind
# mount resolves — otherwise Docker materializes it as a root-owned directory.
mkdir -p "${HOME}/.ssh"
create_if_not_exists "${HOME}/.ssh/known_hosts"

python3 "${SCRIPT_DIR}/initialize.py"
