#!/bin/bash
# Runs on the host machine when the devcontainer is created or started. It can be used to set up environment variables, run scripts, or perform any necessary initialization tasks before the container is fully up and running.

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
export ENV_DIR="${SCRIPT_DIR}/../.envs"
mkdir -p "${ENV_DIR}"

# Ensure the project-level .generated/ exists so the evg-host-proxy bind mount
# resolves cleanly even before the first switch_context run.
mkdir -p "${SCRIPT_DIR}/../../.generated"

COMPOSE_OVERRIDE_FILE="${SCRIPT_DIR}/../compose.generated.yml"
export COMPOSE_OVERRIDE_FILE

function create_if_not_exists() {
    [[ ! -f "$1" ]] && touch "$1"
}
create_if_not_exists "${COMPOSE_OVERRIDE_FILE}"
create_if_not_exists "${SCRIPT_DIR}/../compose.user.yml"

for file in .devcontainer/scripts/initialize/*.sh; do
    bash "${file}"
done
