#!/usr/bin/env bash

# Logs Docker into the search rapid-release ECR registry so that the pull secret created later in
# the e2e task (image-registries-secret) carries credentials for the enterprise mongot image.
#
# Requires the credentials written by write-devprod-platforms-ecr-aws-profile.sh: the DevProd
# Platforms ECR read-only role is assumable from any Evergreen project, and grants read access to
# the whole registry (see https://docs.devprod.prod.corp.mongodb.com/devprod-platforms-ecr).

set -Eeou pipefail

SEARCH_RAPID_RELEASES_REGISTRY="${SEARCH_RAPID_RELEASES_REGISTRY:-901841024863.dkr.ecr.us-east-1.amazonaws.com}"
SEARCH_RAPID_RELEASES_AWS_PROFILE="${SEARCH_RAPID_RELEASES_AWS_PROFILE:-devprod-platforms-ecr}"
DOCKER_CONFIG_FILE="${HOME}/.docker/config.json"

mkdir -p "$(dirname "${DOCKER_CONFIG_FILE}")"
if [[ ! -f "${DOCKER_CONFIG_FILE}" ]]; then
  echo '{}' > "${DOCKER_CONFIG_FILE}"
fi

# Evergreen hosts configure a Docker credential store, which keeps the auth out of config.json.
# The pull secret is built from that file, so drop the store before logging in.
if jq -e 'has("credsStore") or has("credHelpers")' "${DOCKER_CONFIG_FILE}" > /dev/null; then
  tmp_config_file=$(mktemp)
  jq 'del(.credsStore, .credHelpers)' "${DOCKER_CONFIG_FILE}" > "${tmp_config_file}"
  mv "${tmp_config_file}" "${DOCKER_CONFIG_FILE}"
fi

aws --profile "${SEARCH_RAPID_RELEASES_AWS_PROFILE}" ecr get-login-password --region us-east-1 \
  | docker login --username AWS --password-stdin "${SEARCH_RAPID_RELEASES_REGISTRY}"
