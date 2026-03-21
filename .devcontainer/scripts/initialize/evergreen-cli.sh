#!/bin/bash

set -euo pipefail

EVERGREEN_CLI_URL_SET=$(yq '.services.devcontainer.build.args[]? | select(. == "*EVERGREEN_CLI_URL=*")' "${COMPOSE_OVERRIDE_FILE}" 2>/dev/null || true)
if [ -n "${EVERGREEN_CLI_URL_SET}" ]; then
    echo "EVERGREEN_CLI_URL is already set in $(basename "${COMPOSE_OVERRIDE_FILE}"), skipping Evergreen CLI setup. To force a refresh, remove the existing EVERGREEN_CLI_URL entry and reopen the devcontainer."
    exit 0
fi

if ! command -v evergreen &> /dev/null; then
    echo "Evergreen CLI not found, skipping devcontainer Evergreen CLI setup"
    exit 0
fi

ARCH=$(uname -m)
if [ "${ARCH}" = "x86_64" ]; then
    ARCH="amd64"
elif [ "${ARCH}" = "aarch64" ]; then
    ARCH="arm64"
fi

CLI_URL="https://evg-bucket-evergreen.s3.amazonaws.com/evergreen/clients/evergreen_$(evergreen version --build-revision)/linux_${ARCH}/evergreen"
# Check if args is null or doesn't exist, and initialize as empty array
ARGS_VALUE=$(yq '.services.devcontainer.build.args' "${COMPOSE_OVERRIDE_FILE}")
if [ "${ARGS_VALUE}" = "null" ] || [ -z "${ARGS_VALUE}" ]; then
    yq -i '.services.devcontainer.build.args = []' "${COMPOSE_OVERRIDE_FILE}"
fi
# Append the EVERGREEN_CLI_URL
yq -i '.services.devcontainer.build.args += ["EVERGREEN_CLI_URL='"${CLI_URL}"'"]' "${COMPOSE_OVERRIDE_FILE}"
echo "Added EVERGREEN_CLI_URL=${CLI_URL} to $(basename "${COMPOSE_OVERRIDE_FILE}")"
