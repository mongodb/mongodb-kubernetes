#!/usr/bin/env bash

set -Eeou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

source scripts/dev/set_env_context.sh
source scripts/funcs/checks
source scripts/funcs/printing
source scripts/funcs/kubernetes

# Detect available container runtime
detect_container_runtime() {
  if command -v podman &> /dev/null && (podman info &> /dev/null || sudo podman info &> /dev/null); then
    CONTAINER_RUNTIME="podman"
    # Use root's auth.json since minikube uses sudo podman
    CONFIG_PATH="/root/.config/containers/auth.json"
    sudo mkdir -p "$(dirname "${CONFIG_PATH}")"
    echo "Using Podman for container authentication (sudo mode)"
    return 0
  elif command -v docker &> /dev/null; then
    CONTAINER_RUNTIME="docker"
    CONFIG_PATH="${HOME}/.docker/config.json"
    mkdir -p "$(dirname "${CONFIG_PATH}")"
    echo "Using Docker for container authentication"
    return 0
  else
    echo "Error: Neither Docker nor Podman is available"
    exit 1
  fi
}

check_docker_daemon_is_running() {
  if [[ "${CONTAINER_RUNTIME}" == "podman" ]]; then
    # Podman doesn't require a daemon
    echo "Using Podman (no daemon required)"
    return 0
  fi

  if [[ "$(uname -s)" != "Linux" ]]; then
    echo "Skipping docker daemon check when not running in Linux"
    return 0
  fi

  if systemctl is-active --quiet docker; then
      echo "Docker is already running."
  else
      echo "Docker is not running. Starting Docker..."
      # Start the Docker daemon
      sudo systemctl start docker
      for _ in {1..15}; do
        if systemctl is-active --quiet docker; then
            echo "Docker started successfully."
            return 0
        fi
        echo "Waiting for Docker to start..."
        sleep 3
      done
  fi
}

remove_element() {
  config_option="${1}"
  tmpfile=$(mktemp)

  # Initialize config file if it doesn't exist
  if [[ ! -f "${CONFIG_PATH}" ]]; then
    if [[ "${CONFIG_PATH}" == "/root/.config/containers/auth.json" ]]; then
      echo '{}' | sudo tee "${CONFIG_PATH}" > /dev/null
    else
      echo '{}' > "${CONFIG_PATH}"
    fi
  fi

  if [[ "${CONFIG_PATH}" == "/root/.config/containers/auth.json" ]]; then
    sudo "${PROJECT_DIR:-.}/bin/jq" 'del(.'"${config_option}"')' "${CONFIG_PATH}" >"${tmpfile}"
    sudo cp "${tmpfile}" "${CONFIG_PATH}"
  else
    "${PROJECT_DIR:-.}/bin/jq" 'del(.'"${config_option}"')' "${CONFIG_PATH}" >"${tmpfile}"
    cp "${tmpfile}" "${CONFIG_PATH}"
  fi
  rm "${tmpfile}"
}

# Container runtime login wrapper
container_login() {
  local username="$1"
  local registry="$2"

  if [[ "${CONTAINER_RUNTIME}" == "podman" ]]; then
    sudo podman login --authfile "${CONFIG_PATH}" --username "${username}" --password-stdin "${registry}"
  else
    docker login --username "${username}" --password-stdin "${registry}"
  fi
}

# This is the script which performs container authentication to different registries that we use (so far ECR and RedHat)
# As the result of this login the config file will have all the 'auth' information necessary to work with container registries

# Detect container runtime and set appropriate config path
detect_container_runtime

check_docker_daemon_is_running

# Initialize config file if it doesn't exist
if [[ ! -f "${CONFIG_PATH}" ]]; then
  if [[ "${CONFIG_PATH}" == "/root/.config/containers/auth.json" ]]; then
    echo '{}' | sudo tee "${CONFIG_PATH}" > /dev/null
  else
    echo '{}' > "${CONFIG_PATH}"
  fi
fi

if [[ -f "${CONFIG_PATH}" ]]; then
  if [[ "${RUNNING_IN_EVG:-"false"}" != "true" ]]; then
    # Check if login is actually required by making a HEAD request to ECR using existing credentials
    echo "Checking if container registry credentials are valid..."
    if [[ "${CONFIG_PATH}" == "/root/.config/containers/auth.json" ]]; then
      ecr_auth=$(sudo "${PROJECT_DIR:-.}/bin/jq" -r '.auths."268558157000.dkr.ecr.us-east-1.amazonaws.com".auth // empty' "${CONFIG_PATH}")
    else
      ecr_auth=$("${PROJECT_DIR:-.}/bin/jq" -r '.auths."268558157000.dkr.ecr.us-east-1.amazonaws.com".auth // empty' "${CONFIG_PATH}")
    fi

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

  # There could be some leftovers on Evergreen (Docker-specific, skip for Podman)
  if [[ "${CONTAINER_RUNTIME}" == "docker" ]]; then
    if [[ "${CONFIG_PATH}" == "/root/.config/containers/auth.json" ]]; then
      if sudo grep -q "credsStore" "${CONFIG_PATH}"; then
        remove_element "credsStore"
      fi
      if sudo grep -q "credHelpers" "${CONFIG_PATH}"; then
        remove_element "credHelpers"
      fi
    else
      if grep -q "credsStore" "${CONFIG_PATH}"; then
        remove_element "credsStore"
      fi
      if grep -q "credHelpers" "${CONFIG_PATH}"; then
        remove_element "credHelpers"
      fi
    fi
  fi
fi


echo "$(aws --version)}"

aws ecr get-login-password --region "us-east-1" | container_login "AWS" "268558157000.dkr.ecr.us-east-1.amazonaws.com"

# by default docker tries to store credentials in an external storage (e.g. OS keychain) - not in the config.json
# We need to store it as base64 string in config.json instead so we need to remove the "credsStore" element
# This is Docker-specific behavior, Podman stores credentials directly in auth.json
if [[ "${CONTAINER_RUNTIME}" == "docker" ]] && (([[ "${CONFIG_PATH}" == "/root/.config/containers/auth.json" ]] && sudo grep -q "credsStore" "${CONFIG_PATH}") || ([[ "${CONFIG_PATH}" != "/root/.config/containers/auth.json" ]] && grep -q "credsStore" "${CONFIG_PATH}")); then
  remove_element "credsStore"

  # login again to store the credentials into the config.json
  aws ecr get-login-password --region "us-east-1" | container_login "AWS" "268558157000.dkr.ecr.us-east-1.amazonaws.com"
fi

aws ecr get-login-password --region "eu-west-1" | container_login "AWS" "268558157000.dkr.ecr.eu-west-1.amazonaws.com"

if [[ -n "${COMMUNITY_PRIVATE_PREVIEW_PULLSECRET_DOCKERCONFIGJSON:-}" ]]; then
  # log in to quay.io for the mongodb/mongodb-search-community private repo
  # TODO remove once we switch to the official repo in Public Preview
  quay_io_auth_file=$(mktemp)
  config_tmp=$(mktemp)
  echo "${COMMUNITY_PRIVATE_PREVIEW_PULLSECRET_DOCKERCONFIGJSON}" | base64 -d > "${quay_io_auth_file}"
  if [[ "${CONFIG_PATH}" == "/root/.config/containers/auth.json" ]]; then
    sudo "${PROJECT_DIR:-.}/bin/jq" -s '.[0] * .[1]' "${quay_io_auth_file}" "${CONFIG_PATH}" > "${config_tmp}"
    sudo mv "${config_tmp}" "${CONFIG_PATH}"
  else
    jq -s '.[0] * .[1]' "${quay_io_auth_file}" "${CONFIG_PATH}" > "${config_tmp}"
    mv "${config_tmp}" "${CONFIG_PATH}"
  fi
  rm "${quay_io_auth_file}"
fi

create_image_registries_secret
