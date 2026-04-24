#!/bin/bash
# on-create.sh - Runs inside the container during creation.

set -euo pipefail

# Docker mounts volumes as root by default; fix ownership for the devcontainer user.
DEVCONTAINER_USER="$(whoami)"
for mount_point in $(findmnt --noheadings --raw --output TARGET --type overlay,ext4,xfs,btrfs,tmpfs 2>/dev/null | grep -v '^/$\|^/proc\|^/dev\|^/sys' || true); do
    sudo chown -R "${DEVCONTAINER_USER}:${DEVCONTAINER_USER}" "${mount_point}" 2>/dev/null || true
done

for file in .devcontainer/scripts/on-create/*.sh; do
    bash "${file}"
done
