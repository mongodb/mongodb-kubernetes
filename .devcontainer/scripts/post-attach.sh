#!/bin/bash
# post-attach.sh - Runs every time a user attaches to the devcontainer
#
# Retrieves kubeconfig from Evergreen host if EVG_HOST_NAME is set.

set -euo pipefail

# If the container was just created, the shell won't have been updated with the .bashrc changes made by on-create/00-switch-context.sh so source them manually
[[ -f /workspace/.generated/context.export.env ]] && source /workspace/.generated/context.export.env

# Only run if EVG_HOST_NAME is configured
if [[ -n "${EVG_HOST_NAME:-}" ]]; then
    echo "=== Retrieving kubeconfig from Evergreen host ${EVG_HOST_NAME} ==="
    bash scripts/dev/evg_host.sh get-kubeconfig || {
        echo "Warning: Failed to retrieve kubeconfig from Evergreen host"
        exit 0  # Don't fail container startup
    }
else
    echo "EVG_HOST_NAME not set, skipping kubeconfig retrieval"
fi
