#!/bin/bash
# on-create.sh - Runs inside the container during creation.

set -euo pipefail

# Docker creates named volumes root-owned. Chown only the named volumes of compose.yml: the
# bind-mounted workspace is the host's files.
DEVCONTAINER_USER="$(whoami)"
for mount_point in \
    "${HOME}/venv" \
    /workspace/bin \
    /go/pkg/mod \
    "${HOME}/.cache/go-build" \
    "${HOME}/.cache/uv" \
    "${HOME}/.cache/helm"; do
    [[ -d "${mount_point}" ]] || continue
    sudo chown -R "${DEVCONTAINER_USER}:${DEVCONTAINER_USER}" "${mount_point}" 2>/dev/null || true
done

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# Self-relative: the steps live next to this script.
for file in "${SCRIPT_DIR}"/on-create/*.sh; do
    bash "${file}"
done
