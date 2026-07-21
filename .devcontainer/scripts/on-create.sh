#!/bin/bash
# on-create.sh - Runs inside the container during creation.

set -euo pipefail

# Docker creates named volumes root-owned; fix ownership for the devcontainer
# user. Only the KNOWN named volumes are chowned — never an enumeration of all
# mounts, which on Linux hosts would recurse into the bind-mounted workspace,
# ~/.kanopy and ~/.evergreen.yml and rewrite real host file ownership for any
# dev whose host UID isn't 1000. These paths match compose.yml's named volumes.
DEVCONTAINER_USER="$(whoami)"
for mount_point in \
    /workspace/venv \
    /workspace/bin \
    /go/pkg/mod \
    "${HOME}/.cache/go-build" \
    "${HOME}/.cache/uv" \
    "${HOME}/.cache/helm"; do
    [[ -d "${mount_point}" ]] || continue
    sudo chown -R "${DEVCONTAINER_USER}:${DEVCONTAINER_USER}" "${mount_point}" 2>/dev/null || true
done

for file in .devcontainer/scripts/on-create/*.sh; do
    bash "${file}"
done
