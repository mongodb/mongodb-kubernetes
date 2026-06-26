#!/usr/bin/env bash

set -Eeou pipefail

source scripts/dev/set_env_context.sh

if [ "${KUBE_ENVIRONMENT_NAME}" = "minikube" ]; then
    echo "Deleting minikube cluster"
    "${PROJECT_DIR:-.}/bin/minikube" delete
else
    kind delete clusters --all 2>/dev/null || true
    docker system prune --all --volumes --force
fi
