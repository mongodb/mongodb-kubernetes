#!/usr/bin/env bash

set -Eeoux pipefail

source scripts/dev/set_env_context.sh

if [ "${KUBE_ENVIRONMENT_NAME}" = "kind" ]; then
    docker system prune -a -f
    echo "Deleting Kind cluster"
    kind delete clusters --all
fi

if [ "${KUBE_ENVIRONMENT_NAME}" = "minikube" ]; then
    echo "Deleting minikube cluster"
    "${PROJECT_DIR:-.}/bin/minikube" delete
fi
