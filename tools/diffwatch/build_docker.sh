#!/usr/bin/env bash

set -Eeou pipefail

export TAG=${TAG:-"latest"}
export IMAGE="quay.io/lsierant/diffwatch:${TAG}"

echo "Building and pushing multi-platform Docker image: ${IMAGE}"
docker buildx build \
    --platform linux/amd64,linux/arm64 \
    --tag "${IMAGE}" \
    --push \
    .
