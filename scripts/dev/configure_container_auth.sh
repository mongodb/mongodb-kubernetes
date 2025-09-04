#!/usr/bin/env bash

set -Eeou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

source scripts/dev/set_env_context.sh
source scripts/funcs/checks
source scripts/funcs/printing
source scripts/funcs/kubernetes

# Activate venv if it exists (needed for AWS CLI on IBM architectures)
if [[ -f "${PROJECT_DIR}/venv/bin/activate" ]]; then
    source "${PROJECT_DIR}/venv/bin/activate"
fi

CONTAINER_RUNTIME="${CONTAINER_RUNTIME-"docker"}"

# Registry URLs
ECR_EU_WEST="268558157000.dkr.ecr.eu-west-1.amazonaws.com"
ECR_US_EAST="268558157000.dkr.ecr.us-east-1.amazonaws.com"
ECR_SEARCH_US_EAST="901841024863.dkr.ecr.us-east-1.amazonaws.com"

setup_validate_container_runtime() {
  case "${CONTAINER_RUNTIME}" in
    "podman")
      if ! command -v podman &> /dev/null; then
        echo "Error: Podman is not available but was specified"
        exit 1
      fi
      USE_SUDO=true
      CONFIG_PATH="/root/.config/containers/auth.json"
      echo "Using Podman for container authentication (sudo mode)"
      ;;
    "docker")
      if ! command -v docker &> /dev/null; then
        echo "Error: Docker is not available but was specified"
        exit 1
      fi
      USE_SUDO=false
      CONFIG_PATH="${HOME}/.docker/config.json"
      echo "Using Docker for container authentication"
      ;;
    *)
      echo "Error: Invalid container runtime '${CONTAINER_RUNTIME}'. Must be 'docker' or 'podman'"
      exit 1
      ;;
  esac

  if [[ "${USE_SUDO}" == "true" ]]; then
    sudo mkdir -p "$(dirname "${CONFIG_PATH}")"
  else
    mkdir -p "$(dirname "${CONFIG_PATH}")"
  fi
}

# Wrapper function to execute commands with or without sudo
exec_cmd() {
  if [[ "${USE_SUDO}" == "true" ]]; then
    sudo env PATH="${PATH}" "$@"
  else
    "$@"
  fi
}

# Wrapper function to read files with or without sudo
read_file() {
  local file="$1"
  if [[ "${USE_SUDO}" == "true" ]]; then
    sudo cat "${file}"
  else
    cat "${file}"
  fi
}

# Wrapper function to write files with or without sudo
write_file() {
  local content="$1"
  local file="$2"
  if [[ "${USE_SUDO}" == "true" ]]; then
    echo "${content}" | sudo tee "${file}" > /dev/null
  else
    echo "${content}" > "${file}"
  fi
}

remove_element() {
  local config_option="$1"
  local tmpfile
  tmpfile=$(mktemp)

  if [[ ! -f "${CONFIG_PATH}" ]]; then
    write_file '{}' "${CONFIG_PATH}"
  fi

  exec_cmd jq 'del(.'"${config_option}"')' "${CONFIG_PATH}" > "${tmpfile}"
  exec_cmd cp "${tmpfile}" "${CONFIG_PATH}"
  rm "${tmpfile}"
}

registry_login() {
  local username="$1"
  local registry="$2"

  if [[ "${CONTAINER_RUNTIME}" == "podman" ]]; then
    exec_cmd podman login --authfile "${CONFIG_PATH}" --username "${username}" --password-stdin "${registry}"
  else
    docker login --username "${username}" --password-stdin "${registry}"
  fi
}

setup_validate_container_runtime

if [[ ! -f "${CONFIG_PATH}" ]]; then
  write_file '{}' "${CONFIG_PATH}"
fi

check_if_login_required() {
  echo "Checking if container registry credentials are valid..."

  check_registry_credentials() {
    registry_url=$1
    image_path=$1
    image_tag=$2
    # shellcheck disable=SC2016
    ecr_auth=$(exec_cmd jq -r --arg registry "${registry_url}" '.auths.[$registry].auth // empty' "${CONFIG_PATH}")

    if [[ -n "${ecr_auth}" ]]; then
      http_status=$(curl --head -s -o /dev/null -w "%{http_code}" --max-time 3 "https://${registry_url}/v2/${image_path}/manifest/${image_tag}" \
        -H "Authorization: Basic ${ecr_auth}" 2>/dev/null || echo "error/timeout")

      if [[ "${http_status}" != "401" && "${http_status}" != "403" && "${http_status}" != "error/timeout" ]]; then
        echo "Container registry credentials are up to date - not performing the new login!"
        return 0
      fi
      echo -e "${RED}Container login required (HTTP status: ${http_status})${NO_COLOR}"
    else
      echo -e "${RED}No ECR credentials found in container config - login required${NO_COLOR}"
    fi

    return 0
  }

  check_registry_credentials "${ECR_EU_WEST}" "mongot/community" "1.47.0" | prepend "${ECR_EU_WEST}" || return 1
  check_registry_credentials "${ECR_US_EAST}" "dev/mongodb-kubernetes" "latest" | prepend "${ECR_US_EAST}" || return 1
  if [[ "${MDB_SEARCH_AWS_SSO_LOGIN:-"false"}" == "true" ]]; then
    check_registry_credentials "${ECR_SEARCH_US_EAST}" "mongot-community/rapid-releases" "latest" | prepend "${ECR_SEARCH_US_EAST}" || return 1
  fi

  return 0
}

login_to_registries() {
  title "Performing container login to ECR registries"
  echo "$(aws --version)}"

  # There could be some leftovers on Evergreen (Docker-specific, skip for Podman)
  if [[ "${CONTAINER_RUNTIME}" == "docker" ]]; then
    if exec_cmd grep -q "credsStore" "${CONFIG_PATH}"; then
      remove_element "credsStore"
    fi
    if exec_cmd grep -q "credHelpers" "${CONFIG_PATH}"; then
      remove_element "credHelpers"
    fi
  fi

  aws ecr get-login-password --region "us-east-1" | registry_login "AWS" "${ECR_US_EAST}"

  if [[ "${MDB_SEARCH_AWS_SSO_LOGIN:-"false"}" == "true" ]]; then
    aws sso login --profile devprod-platforms-ecr-user
    aws --profile devprod-platforms-ecr-user  ecr get-login-password --region us-east-1 | registry_login "AWS" "${ECR_SEARCH_US_EAST}"
  fi

  # by default docker tries to store credentials in an external storage (e.g. OS keychain) - not in the config.json
  # We need to store it as base64 string in config.json instead so we need to remove the "credsStore" element
  # This is Docker-specific behavior, Podman stores credentials directly in auth.json
  if [[ "${CONTAINER_RUNTIME}" == "docker" ]] && exec_cmd grep -q "credsStore" "${CONFIG_PATH}"; then
    remove_element "credsStore"

    # login again to store the credentials into the config.json
    aws ecr get-login-password --region "us-east-1" | registry_login "AWS" "${ECR_US_EAST}"
  fi

  aws ecr get-login-password --region "eu-west-1" | registry_login "AWS" "${ECR_EU_WEST}"

  if [[ -n "${PRERELEASE_PULLSECRET_DOCKERCONFIGJSON:-}" ]]; then
    # log in to quay.io for the mongodb/mongodb-search-community private repo
    # TODO remove once we switch to the official repo in Public Preview
    quay_io_auth_file=$(mktemp)
    config_tmp=$(mktemp)
    echo "${PRERELEASE_PULLSECRET_DOCKERCONFIGJSON}" | base64 -d > "${quay_io_auth_file}"
    exec_cmd jq -s '.[0] * .[1]' "${quay_io_auth_file}" "${CONFIG_PATH}" > "${config_tmp}"
    exec_cmd mv "${config_tmp}" "${CONFIG_PATH}"
    rm "${quay_io_auth_file}"
  fi
}

if [[ "${RUNNING_IN_EVG:-"false"}" != "true" ]]; then
  check_if_login_required
  login_to_registries
fi

create_image_registries_secret
