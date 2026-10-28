#!/bin/bash
# Runs on the host before the devcontainer starts (devcontainer.json
# initializeCommand; also wt-ctl's initialize_hook phase). It locates the
# repository from its own path, so the working directory does not matter.
# Everything it writes (compose.generated.yml, compose.user.yml) is gitignored.
# Safe to run repeatedly.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
DEVC_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
REPO_ROOT="$(cd "${DEVC_DIR}/.." && pwd)"

# Create .generated/ first: Docker would create the evg-host-proxy bind mount of it root-owned.
# .generated/ssh/known_hosts holds the Evergreen host key that the autossh sidecar checks.
mkdir -p "${REPO_ROOT}/.generated/ssh"

COMPOSE_OVERRIDE_FILE="${DEVC_DIR}/compose.generated.yml"
export COMPOSE_OVERRIDE_FILE

function create_if_not_exists() {
    [[ -f "$1" ]] || touch "$1"
}
create_if_not_exists "${COMPOSE_OVERRIDE_FILE}"
create_if_not_exists "${DEVC_DIR}/compose.user.yml"

# initialize.py asks git for the common dir of the cwd's repository.
cd "${REPO_ROOT}"
python3 "${SCRIPT_DIR}/initialize.py"
