#!/usr/bin/env bash

set -Eeou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

source scripts/dev/set_env_context.sh
source scripts/funcs/checks
source scripts/funcs/printing
source scripts/funcs/kubernetes

setup_validate_container_runtime() {
  if ! command -v docker &> /dev/null; then
    echo "Error: Docker is not available"
    exit 1
  fi
  CONFIG_PATH="${HOME}/.docker/config.json"
  echo "Using Docker for container authentication"
  mkdir -p "$(dirname "${CONFIG_PATH}")"
}

remove_element() {
  local config_option="$1"
  local tmpfile
  tmpfile=$(mktemp)

  if [[ ! -f "${CONFIG_PATH}" ]]; then
    echo '{}' > "${CONFIG_PATH}"
  fi

  jq 'del(.'"${config_option}"')' "${CONFIG_PATH}" > "${tmpfile}"
  cp "${tmpfile}" "${CONFIG_PATH}"
  rm "${tmpfile}"
}

registry_login() {
  local username="$1"
  local registry="$2"
  docker login --username "${username}" --password-stdin "${registry}"
}

setup_validate_container_runtime

if [[ ! -f "${CONFIG_PATH}" ]]; then
  echo '{}' > "${CONFIG_PATH}"
fi

if [[ -f "${CONFIG_PATH}" ]]; then
  if [[ "${RUNNING_IN_EVG:-"false"}" != "true" ]]; then
    echo "Checking if container registry credentials are valid..."
    ecr_auth=$(jq -r '.auths."268558157000.dkr.ecr.us-east-1.amazonaws.com".auth // empty' "${CONFIG_PATH}")

    if [[ -n "${ecr_auth}" ]]; then
      http_status=$(curl --head -s -o /dev/null -w "%{http_code}" --max-time 3 "https://268558157000.dkr.ecr.us-east-1.amazonaws.com/v2/dev/mongodb-kubernetes/manifests/latest" \
        -H "Authorization: Basic ${ecr_auth}" 2>/dev/null || echo "error/timeout")

      if [[ "${http_status}" != "401" && "${http_status}" != "403" && "${http_status}" != "error/timeout" ]]; then
        echo "Container registry credentials are up to date - not performing the new login!"
        exit
      fi
      echo "Container login required (HTTP status: ${http_status})"
    else
      echo "No ECR credentials found in container config - login required"
    fi
  fi

  title "Performing container login to ECR registries"

  # There could be some leftovers on Evergreen
  if grep -q "credsStore" "${CONFIG_PATH}"; then
    remove_element "credsStore"
  fi
  if grep -q "credHelpers" "${CONFIG_PATH}"; then
    remove_element "credHelpers"
  fi
fi


echo "$(aws --version)}"

aws ecr get-login-password --region "us-east-1" | registry_login "AWS" "268558157000.dkr.ecr.us-east-1.amazonaws.com"

# by default docker tries to store credentials in an external storage (e.g. OS keychain) - not in the config.json
# We need to store it as base64 string in config.json instead so we need to remove the "credsStore" element
if grep -q "credsStore" "${CONFIG_PATH}"; then
  remove_element "credsStore"

  # login again to store the credentials into the config.json
  aws ecr get-login-password --region "us-east-1" | registry_login "AWS" "268558157000.dkr.ecr.us-east-1.amazonaws.com"
fi

aws ecr get-login-password --region "eu-west-1" | registry_login "AWS" "268558157000.dkr.ecr.eu-west-1.amazonaws.com"

if [[ -n "${COMMUNITY_PRIVATE_PREVIEW_PULLSECRET_DOCKERCONFIGJSON:-}" ]]; then
  # log in to quay.io for the mongodb/mongodb-search-community private repo
  # TODO remove once we switch to the official repo in Public Preview
  quay_io_auth_file=$(mktemp)
  config_tmp=$(mktemp)
  echo "${COMMUNITY_PRIVATE_PREVIEW_PULLSECRET_DOCKERCONFIGJSON}" | base64 -d > "${quay_io_auth_file}"
  jq -s '.[0] * .[1]' "${quay_io_auth_file}" "${CONFIG_PATH}" > "${config_tmp}"
  mv "${config_tmp}" "${CONFIG_PATH}"
  rm "${quay_io_auth_file}"
fi

create_image_registries_secret
