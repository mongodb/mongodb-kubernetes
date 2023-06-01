#!/usr/bin/env bash

set -Eeou pipefail

source ./scripts/dev/set_env_context.sh

if [ -z "${CLUSTER_NAME-}" ]; then
    echo "Skipping setting cluster_name"
else
    # The convention: the cluster name must match the name of kubectl context
    # We expect this not to be true if kubernetes cluster is still to be created (minikube/kops)
    if ! kubectl config use-context "${CLUSTER_NAME}"; then
        echo "Warning: failed to switch kubectl context to: ${CLUSTER_NAME}"
        echo "Does a matching Kubernetes context exist?"
    fi

    # Setting the default namespace for current context
    kubectl config set-context "$(kubectl config current-context)" "--namespace=${NAMESPACE}" &>/dev/null || true

    echo "Current context: ${CURRENT_CONTEXT} (kubectl context: ${CLUSTER_NAME}), namespace=${NAMESPACE}"
fi
