#!/usr/bin/env bash

## redo.sh
##
##   developer's script to reinstall the operator
##

set -euo pipefail

NAMESPACE_PREFIX="my-namespace"
MINIKUBE_CONTEXT="minikube"

rollback() {
    echo "Exiting due to $1"
    kubectl delete ns "${NAMESPACE}"

    exit 1
}

use_new_namespace () {
    redo_lastns_file=".redo-last-namespace"
    namespace_idx="0"
    if [ -f "${redo_lastns_file}" ]; then
        namespace_idx="$(cat ${redo_lastns_file})"
    fi
    namespace_idx=$((namespace_idx+1))
    echo "${namespace_idx}" > "${redo_lastns_file}"
    echo "${NAMESPACE_PREFIX}-${namespace_idx}"
}

install_ops_manager() {
    # this needs to be running

    if kubectl get ns | grep -q operator-testing; then
        # TODO: check if the ops manager pod is actually running
        echo "Ops Manager is already installed in this cluster"
        return
    fi

    if ! curl -sSf http://localhost:8000 > /dev/null; then
        echo "Make sure the operator docker image cache server is running!"
        echo "cd docker/mongodb-enterprise-operator/ && make cache_server"
        echo "and leave it running in the background."

        exit 1
    fi
    # TODO: wait until the cache server has already downloaded mms + mongodb
    make -C docker/mongodb-enterprise-ops-manager build_cached
    # minikube -p "${MINIKUBE_CONTEXT}" cache add mongodb-enterprise-ops-manager:4.0.5.50245.20181031T0042Z-1_test

    kubectl create ns operator-testing

    echo "Deploying Ops Manger in the kubernetes cluster, this might take a bit"
    kubectl -n operator-testing apply -f scripts/evergreen/deployments/mongodb-enterprise-ops-manager.yaml
    echo "We are going to leave Ops Manager alone for a few minutes, while it start."
}

get_ops_manager_exposed() {
    OPS_MANAGER_PORT="$(kubectl -n operator-testing get services/mongodb-enterprise-ops-manager-0 -o jsonpath='{.spec.ports[0].nodePort}')"
    OPS_MANAGER_IP=$(kubectl cluster-info | grep -i "is running at" | grep master | awk '{ print $NF}' | cut -d "/" -f 3 | cut -d ":" -f 1)

    echo "http://${OPS_MANAGER_IP}:${OPS_MANAGER_PORT}"
}

setup_minikube() {
    echo "Setting up minikube"
    if ! minikube -p "${MINIKUBE_CONTEXT}" status | grep -q "Running" ; then
        echo "Minikube is not running, starting it now"
        if [[ $(uname -a) =~ "Linux" ]]; then
            minikube -p "${MINIKUBE_CONTEXT}" start --memory 8192 --vm-driver kvm2
        else
            minikube -p "${MINIKUBE_CONTEXT}" start --memory 5120
        fi
    fi
    eval "$(minikube -p ${MINIKUBE_CONTEXT} docker-env)"
}

minikube_cache_image () {
    # 'minikube cache' for an existing image takes about .7 seconds to complete, that's why I first check that the
    # image is not already present in the cache.
    if ! docker images | grep "$1" | grep "$2" > /dev/null; then minikube -p "${MINIKUBE_CONTEXT}" cache add "$1:$2"; fi
}

create_namespace () {
    if kubectl get namespace | grep -q "${NAMESPACE}"; then
        kubectl delete ns "${NAMESPACE}"

        echo "Waiting for ${NAMESPACE} to terminate"
        while kubectl get ns | grep -q "${NAMESPACE}"; do sleep 3; done
    fi

    echo "Creating namespace ${NAMESPACE}"
    kubectl create ns "${NAMESPACE}"
}

build_images () {
    echo "Building operator & database images"
    make -C docker/mongodb-enterprise-database build IMAGE_DIR=local || rollback "failed building database image"
    make -C docker/mongodb-enterprise-operator build IMAGE_DIR=local || rollback "failed building operator image"
}

configure_operator () {
    if ! kubectl -n operator-testing get pod/mongodb-enterprise-ops-manager-0 | grep -q "1/1"; then
        echo "It is now time to configure the operator, we'll have to wait until Ops Manager is ready for connections."
        while ! kubectl -n operator-testing get pod/mongodb-enterprise-ops-manager-0 | grep -q "1/1"; do sleep 5; done

        echo "Exposing Ops Manager"
        kubectl -n operator-testing expose pod mongodb-enterprise-ops-manager-0 --port 8080 --type NodePort
    fi
    eval "$(kubectl -n operator-testing exec mongodb-enterprise-ops-manager-0 cat /opt/mongodb/mms/env/.ops-manager-env)"

    OM_HOST="http://ops-manager-internal.operator-testing.svc.cluster.local:8080"

    echo "Configuring Operator (project and credentials)"
    kubectl --namespace "${NAMESPACE}" create secret generic my-credentials \
            --from-literal=user="${OM_USER:=admin}" \
            --from-literal=publicApiKey="${OM_API_KEY}"

    kubectl --namespace "${NAMESPACE}" create configmap my-project \
            --from-literal=projectName="${NAMESPACE}" \
            --from-literal=baseUrl="${OM_HOST}"
}

install_operator () {
    # TODO: generate the yaml files with helm (passing the right values for versions)
    outdir="helm_out"
    mkdir -p "${outdir}"

    helm template \
         --set namespace="" \
         --set operator.version="latest" \
         --set registry.repository="local" \
         --set registry.pullPolicy="IfNotPresent" \
         --set operator.env="dev" \
         --set managedSecurityContext="${MANAGED_SECURITY_CONTEXT:-false}" \
         -f scripts/evergreen/deployments/values-test.yaml public/helm_chart --output-dir "${outdir}"

    template_dir="${outdir}/mongodb-enterprise-operator/templates"
    echo "Installing CRD"
    if ! kubectl get crd | grep -q "mongodb"; then
        kubectl apply -f "${template_dir}/crds.yaml"
    fi

    echo "Installing Operator"
    for i in serviceaccount roles operator; do
        kubectl -n "${NAMESPACE}" apply -f "${template_dir}/${i}.yaml"
    done
}


NAMESPACE="$(use_new_namespace)"
echo "Using new namespace ${NAMESPACE}"

setup_minikube

echo "Caching images locally if needed"
minikube_cache_image "debian" "jessie-slim"
minikube_cache_image "debian" "9.5-slim"

create_namespace
install_ops_manager
build_images
install_operator
configure_operator

echo "You can visit Ops Manager UI at $(get_ops_manager_exposed)"

echo "Waiting for operator to reach Running state"
OPERATOR_POD_NAME=$(kubectl -n "${NAMESPACE}" get pods | grep "mongodb-enterprise-operator" | awk '{print $1}')
while ! kubectl -n "${NAMESPACE}" get "pod/${OPERATOR_POD_NAME}" | grep -q "Running"; do sleep 1; done

kubectl -n "${NAMESPACE}" logs "${OPERATOR_POD_NAME}" -f
