#!/usr/bin/env bash

set -Eeou pipefail

export TAG=${TAG:-"latest"}

docker build --platform linux/amd64 -t "quay.io/lsierant/diffwatch:${TAG}" .
docker push "quay.io/lsierant/diffwatch:${TAG}"
