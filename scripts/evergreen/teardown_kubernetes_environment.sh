#!/usr/bin/env bash

set -Eeou pipefail

context_config="${workdir:?}/${kube_environment_name:?}_config"

if [ "${kube_environment_name}" = "kind" ]; then
    echo "Deleting Kind cluster"
    kind delete cluster
elif [[ "${kube_environment_name}" = "minikube" ]]; then
    echo "Deleting Minikube cluster"
    minikube delete
fi

if [ -f "${context_config}" ]; then
    rm "${context_config}"
fi
