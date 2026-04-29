#!/usr/bin/env bash

set -Eeou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

if [[ -z "${QUAY_STAGING_USERNAME:-}" || -z "${QUAY_STAGING_PASSWORD:-}" ]]; then
  echo "Error: QUAY_STAGING_USERNAME and QUAY_STAGING_PASSWORD must be set"
  exit 1
fi

# IBM hosts (ppc64le, s390x) run rootful podman; configure_container_auth.sh writes
# credentials to /root/.config/containers/auth.json via `sudo podman login`. Match
# that here so the buildx imagetools inspect/push calls find quay.io credentials.
if [[ -z "${CONTAINER_RUNTIME:-}" ]]; then
  case "$(uname -m)" in
    ppc64le|s390x) CONTAINER_RUNTIME=podman ;;
    *) CONTAINER_RUNTIME=docker ;;
  esac
fi

case "${CONTAINER_RUNTIME}" in
  podman)
    if ! command -v podman &> /dev/null; then
      echo "Error: Podman is not available but was specified"
      exit 1
    fi
    echo "${QUAY_STAGING_PASSWORD}" | sudo podman login \
      --authfile /root/.config/containers/auth.json \
      --username "${QUAY_STAGING_USERNAME}" \
      --password-stdin quay.io
    ;;
  docker)
    if ! command -v docker &> /dev/null; then
      echo "Error: Docker is not available but was specified"
      exit 1
    fi
    echo "${QUAY_STAGING_PASSWORD}" | docker login \
      --username "${QUAY_STAGING_USERNAME}" \
      --password-stdin quay.io
    ;;
  *)
    echo "Error: Invalid container runtime '${CONTAINER_RUNTIME}'. Must be 'docker' or 'podman'"
    exit 1
    ;;
esac
