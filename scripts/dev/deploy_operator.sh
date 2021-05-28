#!/usr/bin/env bash

set -Eeou pipefail


source scripts/dev/set_env_context.sh
source scripts/funcs/errors
source scripts/funcs/kubernetes
source scripts/funcs/printing

export DEBUG="${1-}"

title "Deploying the Operator to Kubernetes cluster..."

# checking the status of k8s cluster
if [[ ${CLUSTER_TYPE} = "kops" ]]; then
    # shellcheck disable=2153
    if kops validate cluster "${CLUSTER_NAME}" | grep -q "not found"; then
        fatal "kops cluster is not running, call \"make\" to create it"
    fi
elif [[ ${CLUSTER_TYPE} = "openshift" ]]; then
    if ! kubectl get nodes &> /dev/null; then
        fatal "Could not communicate with Openshift cluster"
    fi
elif [[ ${CLUSTER_TYPE} = "kind" ]]; then
    if ! kubectl get nodes &> /dev/null; then
        fatal "Could not communicate with Kind cluster"
    fi
else
    fatal "CLUSTER_TYPE=${CLUSTER_TYPE} not recognized"
fi

# making sure the namespace is created
ensure_namespace "${NAMESPACE}"

#installing the operator
if [[ ${CLUSTER_TYPE} = "openshift" ]]; then
    managed_security_context=true
fi

if [[ ${IMAGE_TYPE} = "ubi" ]]; then
    # we should use the UBI images with special names if quay.io is used as a source
    if [[ "${OPS_MANAGER_REGISTRY}" == quay.io* ]]; then
      OPS_MANAGER_NAME=mongodb-enterprise-ops-manager-ubi
    fi
    if [[ "${DATABASE_REGISTRY}" == quay.io* ]]; then
      DATABASE_NAME=mongodb-enterprise-database-ubi
    fi
fi

# We always create the image pull secret from the docker config.json which gives access to all necesary image repositories
echo "Creating/updating pull secret from docker configured file"
kubectl -n "${NAMESPACE}" delete secret image-registries-secret --ignore-not-found
kubectl -n "${NAMESPACE}" create secret generic image-registries-secret \
  --from-file=.dockerconfigjson="${HOME}/.docker/config.json" --type=kubernetes.io/dockerconfigjson

## Delete Operator
delete_operator "${NAMESPACE:-mongodb}"

## Deploy Operator using Helm
title "Installing the Operator to ${CLUSTER_NAME:-'e2e cluster'}..."

check_app "helm" "helm is not installed, run 'make prerequisites' to install all necessary software"
check_app "timeout" "coreutils is not installed, call \"brew install coreutils\""

helm_params=(
     "--set" "registry.operator=${REPO_URL:?}"
     "--set" "registry.opsManager=${OPS_MANAGER_REGISTRY:?}"
     "--set" "registry.appDb=${APPDB_REGISTRY:?}"
     "--set" "registry.database=${DATABASE_REGISTRY:?}"
     "--set" "registry.initOpsManager=${INIT_OPS_MANAGER_REGISTRY:?}"
     "--set" "registry.initAppDb=${INIT_APPDB_REGISTRY:?}"
     "--set" "registry.initDatabase=${INIT_DATABASE_REGISTRY:?}"
     "--set" "registry.pullPolicy=${pull_policy:-Always}"
     "--set" "operator.env=dev"
     "--set" "operator.version=${OPERATOR_VERSION:-latest}"
     "--set" "operator.watchNamespace=${watch_namespace:-$NAMESPACE}"
     "--set" "operator.name=${OPERATOR_NAME:=mongodb-enterprise-operator}"
     "--set" "database.name=${DATABASE_NAME:=mongodb-enterprise-database}"
     "--set" "opsManager.name=${OPS_MANAGER_NAME:=mongodb-enterprise-ops-manager}"
     "--set" "initDatabase.version=latest"
     "--set" "initOpsManager.name=${INIT_OPS_MANAGER_NAME:=mongodb-enterprise-init-ops-manager}"
     "--set" "initOpsManager.version=latest"
     "--set" "initAppDb.name=${INIT_APPDB_NAME:=mongodb-enterprise-init-appdb}"
     "--set" "initAppDb.version=latest"
     "--set" "namespace=${NAMESPACE}"
     "--set" "managedSecurityContext=${managed_security_context:-false}"
     "--set" "debug=${DEBUG-}"
     "--set" "debugPort=${DEBUG_PORT:-30042}"
     "--set" "registry.imagePullSecrets=image-registries-secret"
)

# setting an empty watched resource to avoid endpoint overriding - this allows to use debug
[[ -n "${DEBUG-}" ]] && helm_params+=("--set" "operator.watchedResources=")

chart_path="public/helm_chart"

echo "Deploying Operator, helm arguments:" "${helm_params[@]}"
kubectl create  -f "${chart_path}/crds" 2>/dev/null || kubectl replace -f "${chart_path}/crds"
helm upgrade --install  "${helm_params[@]}" mongodb-enterprise-operator "${chart_path}"

## Wait for the Operator to start

if ! wait_for_operator_start "${NAMESPACE}" "1m"
then
    echo "Operator failed to start"
    exit 1
fi
