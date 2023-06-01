#!/usr/bin/env bash

set -Eeou pipefail

script_directory=$(dirname "$0")

# script checks if kubectl has the matching contexts - if not - then tries to create Kube cluster
source scripts/dev/read_context.sh
source scripts/funcs/checks
source scripts/funcs/kubernetes
source scripts/funcs/printing

title "Ensuring Kubernetes cluster is up..."

if [[ ${CLUSTER_TYPE} = "kops" ]]; then
	check_app "kops" "kops is not installed!"
elif [[ ${CLUSTER_TYPE} = "kind" ]]; then
	check_app "kind" "kind is not installed!"
fi

if [[ ${CLUSTER_TYPE} = "kops" ]] && ! kops validate cluster "${CLUSTER_NAME}" ; then
	check_app "timeout" "coreutils is not installed, call \"brew install coreutils\""

  # does cluster exist but just not imported to ~/.kube ?
  kops export kubecfg "${CLUSTER_NAME}" || true

  if ! kops validate cluster "${CLUSTER_NAME}"; then
    echo "Kops cluster \"${CLUSTER_NAME}\" doesn't exist"

    create_kops_cluster "${CLUSTER_NAME}" 3 16 "t2.large" "t2.small" "${KOPS_ZONES:-us-east-2a}" "${KOPS_K8S_VERSION:-}"
  fi

elif [[ ${CLUSTER_TYPE} = "openshift" ]]; then
	echo "openshift is TODO"
elif [[ ${CLUSTER_TYPE} = "kind" ]]; then
  "${script_directory}"/recreate_kind_cluster.sh "${CLUSTER_NAME}"
fi

title "Kubernetes cluster ${CLUSTER_NAME} is up"
