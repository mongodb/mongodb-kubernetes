#!/bin/bash

# Forward the host's tmux configuration into the devcontainer if present.
# Each location is mounted only when it exists on the host so a missing config
# doesn't break compose-up. Plugin directories are intentionally skipped: TPM
# plugins may include platform-specific binaries; install them inside the
# container if you need them.

set -euo pipefail

mount_if_exists() {
    local src="$1"
    local dst="$2"
    [[ -e "${src}" ]] || return 0

    local entry="${src}:${dst}:ro"
    if yq eval ".services.devcontainer.volumes[] | select(. == \"${entry}\")" "${COMPOSE_OVERRIDE_FILE}" | grep -q .; then
        return 0
    fi
    yq eval -i ".services.devcontainer.volumes += [\"${entry}\"]" "${COMPOSE_OVERRIDE_FILE}"
    echo "Added tmux config volume: ${entry}"
}

mount_if_exists "${HOME}/.tmux.conf"      "/home/vscode/.tmux.conf"
mount_if_exists "${HOME}/.config/tmux"    "/home/vscode/.config/tmux"
