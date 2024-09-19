#!/usr/bin/env bash

set -Eeou pipefail

docker build --platform linux/amd64 -t quay.io/lsierant/diffwatch:latest .
docker push quay.io/lsierant/diffwatch:latest
