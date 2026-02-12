#!/bin/bash
# Runs on the host machine when the devcontainer is created or started. It can be used to set up environment variables, run scripts, or perform any necessary initialization tasks before the container is fully up and running.

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
export ENV_DIR="${SCRIPT_DIR}/../.envs"
mkdir -p "${ENV_DIR}"

# query the docker containers by label, look for the one where the devcontainer.local_folder label is set to the absolute value of $SCRIPT_DIR/../..
DEV_CONTAINER_ID=$(docker ps --filter "label=devcontainer.config_file=$(realpath "${SCRIPT_DIR}/../devcontainer.json")" --format "{{.ID}}")
export DEV_CONTAINER_ID
COMPOSE_OVERRIDE_FILE="${SCRIPT_DIR}/../compose.generated.yml"
export COMPOSE_OVERRIDE_FILE

touch "${COMPOSE_OVERRIDE_FILE}"
touch "${SCRIPT_DIR}/../compose.user.yml"

for file in .devcontainer/scripts/initialize/*.sh; do
    bash "${file}"
done
