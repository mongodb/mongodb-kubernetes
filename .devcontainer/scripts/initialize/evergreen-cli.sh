#!/bin/bash

# Resolve the Linux Evergreen CLI URL on the host (using the host's evergreen
# binary as the version anchor) and inject it as an environment variable on the
# devcontainer service. The actual download happens at on-create time
# (see on-create/05-evergreen-cli.sh) so the image stays stable across version
# bumps and can be pre-built once.

set -euo pipefail

if ! command -v evergreen &> /dev/null; then
    echo "Evergreen CLI not found on host; skipping URL resolution. The on-create"
    echo "step will be a no-op and you'll need to install evergreen manually inside"
    echo "the container."
    exit 0
fi

ARCH=$(uname -m)
if [ "${ARCH}" = "x86_64" ]; then
    ARCH="amd64"
elif [ "${ARCH}" = "aarch64" ]; then
    ARCH="arm64"
fi

CLI_URL="https://evg-bucket-evergreen.s3.amazonaws.com/evergreen/clients/evergreen_$(evergreen version --build-revision)/linux_${ARCH}/evergreen"

# Ensure the environment array exists, then upsert EVERGREEN_CLI_URL.
ENV_VALUE=$(yq '.services.devcontainer.environment' "${COMPOSE_OVERRIDE_FILE}")
if [ "${ENV_VALUE}" = "null" ] || [ -z "${ENV_VALUE}" ]; then
    yq -i '.services.devcontainer.environment = []' "${COMPOSE_OVERRIDE_FILE}"
fi
# Strip any prior EVERGREEN_CLI_URL entry, then append the fresh one.
yq -i '.services.devcontainer.environment |= map(select(. | test("^EVERGREEN_CLI_URL=") | not))' "${COMPOSE_OVERRIDE_FILE}"
yq -i '.services.devcontainer.environment += ["EVERGREEN_CLI_URL='"${CLI_URL}"'"]' "${COMPOSE_OVERRIDE_FILE}"
echo "Set EVERGREEN_CLI_URL=${CLI_URL} on devcontainer service in $(basename "${COMPOSE_OVERRIDE_FILE}")"
