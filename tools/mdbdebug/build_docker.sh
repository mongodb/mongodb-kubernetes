#!/usr/bin/env bash

set -Eeou pipefail

# Default image name and tag
IMAGE_NAME=${IMAGE_NAME:-"mdbdebug"}
IMAGE_TAG=${IMAGE_TAG:-"latest"}
REGISTRY=${REGISTRY:-"quay.io/lsierant"}

script_name=$(readlink -f "${BASH_SOURCE[0]}")
script_dir=$(dirname "${script_name}")

# Change to the script directory
pushd "${script_dir}" >/dev/null 2>&1

./build.sh

echo "Building Docker image ${IMAGE_NAME}:${IMAGE_TAG}"
if [ -z "${REGISTRY}" ]; then
    docker build -t "${IMAGE_NAME}:${IMAGE_TAG}" .
else
    docker build -t "${REGISTRY}/${IMAGE_NAME}:${IMAGE_TAG}" .
    
    echo "Pushing Docker image ${REGISTRY}/${IMAGE_NAME}:${IMAGE_TAG}"
    docker push "${REGISTRY}/${IMAGE_NAME}:${IMAGE_TAG}"
fi

popd >/dev/null 2>&1
