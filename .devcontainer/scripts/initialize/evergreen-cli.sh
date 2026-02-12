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

EVG_AUTH_HEADERS=()
API_KEY=$(evergreen client get-api-key)
if [ -n "${API_KEY}" ]; then
    # API key authentication
    EVG_AUTH_HEADERS+=("-H" "Api-Key: ${API_KEY}")
    EVG_AUTH_HEADERS+=("-H" "Api-User: $(evergreen client get-user)")
else
    # OAuth token authentication
    OAUTH_TOKEN=$(evergreen client get-oauth-token)
    if [ -n "${OAUTH_TOKEN}" ]; then
        # select only the last line from the output, because the initial device flow may include other instructions
        OAUTH_TOKEN=$(echo "${OAUTH_TOKEN}" | tail -n 1)
        EVG_AUTH_HEADERS+=("-H" "Authorization: Bearer ${OAUTH_TOKEN}")
    fi
fi

CLI_VERSION_URL="$(evergreen client get-api-url)/rest/v2/status/cli_version"
CLI_VERSION_RESPONSE=$(curl -s "${EVG_AUTH_HEADERS[@]}" "${CLI_VERSION_URL}")
CLI_URL=$(echo "${CLI_VERSION_RESPONSE}" | jq -r --arg ARCH "$(uname -m)" '.client_config.client_binaries[] | select(.os == "linux" and .arch == ($ARCH | if . == "x86_64" then "amd64" elif . == "aarch64" then "arm64" else . end)) | .url')
if [ -n "${CLI_URL}" ]; then
    # Check if args is null or doesn't exist, and initialize as empty array
    ARGS_VALUE=$(yq '.services.devcontainer.build.args' "${COMPOSE_OVERRIDE_FILE}")
    if [ "${ARGS_VALUE}" = "null" ] || [ -z "${ARGS_VALUE}" ]; then
        yq -i '.services.devcontainer.build.args = []' "${COMPOSE_OVERRIDE_FILE}"
    fi
    # Append the EVERGREEN_CLI_URL
    yq -i '.services.devcontainer.build.args += ["EVERGREEN_CLI_URL='"${CLI_URL}"'"]' "${COMPOSE_OVERRIDE_FILE}"
    echo "Added EVERGREEN_CLI_URL=${CLI_URL} to $(basename "${COMPOSE_OVERRIDE_FILE}")"
else
    echo "Could not find Evergreen CLI URL for Linux $(uname -m) in API response"
fi
