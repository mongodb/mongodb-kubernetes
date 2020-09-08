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
if [[ ${REPO_TYPE} = "local" ]]; then
    pull_policy="Never"
fi

if [[ ${CLUSTER_TYPE} = "openshift" ]]; then
    managed_security_context=true

    # for Openshift we use images from UBI repos (the same registry quay.io though)
    #
    # The following 2 variables are set here and read from the `deploy_operator`
    # function.
    OPS_MANAGER_NAME=mongodb-enterprise-ops-manager-ubi
    APPDB_NAME=mongodb-enterprise-appdb-ubi
fi

# For any cluster except for kops (Kind, Openshift) access to ECR registry needs authorization - hence
# creating the image pull secret
if [[ ${CLUSTER_TYPE} != "kops" ]] && [[ ${REPO_URL} == *".ecr."* ]]; then
    export ecr_registry_needs_auth="ecr-registry-secret"
    docker_config=$(mktemp)

    scripts/dev/configure_docker "${REPO_URL}" > "${docker_config}"

    echo "Creating/updating pull secret from docker configured file"
    kubectl -n "${NAMESPACE}" create secret generic "ecr-registry-secret" \
		--from-file=.dockerconfigjson="${docker_config}" --type=kubernetes.io/dockerconfigjson --dry-run -o yaml | \
		 kubectl apply -f -
    rm "${docker_config}"
fi

delete_operator "${NAMESPACE:-mongodb}"
deploy_operator \
    "${REPO_URL:?}" \
    "${INIT_OPS_MANAGER_REGISTRY:?}" \
    "${INIT_APPDB_REGISTRY:?}" \
    "${INIT_DATABASE_REGISTRY:?}" \
    "${OPS_MANAGER_REGISTRY}" \
    "${APPDB_REGISTRY:?}" \
    "${DATABASE_REGISTRY:?}" \
    "${NAMESPACE}" \
    "${OPERATOR_VERSION:-latest}" \
    "${watch_namespace:-$NAMESPACE}" \
    "${pull_policy:-}" \
    "${managed_security_context:-}"

if ! wait_for_operator_start "${NAMESPACE}" "1m"
then
    echo "Operator failed to start"
    exit 1
fi
