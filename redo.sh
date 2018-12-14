#!/usr/bin/env bash

## redo.sh
##
##   developer's script to reinstall the operator
##

set -euo pipefail

NAMESPACE_PREFIX="my-namespace"
MINIKUBE_CONTEXT="minikube"

# Array contains string; based on https://stackoverflow.com/questions/3685970/check-if-a-bash-array-contains-a-value
contains() {
    local e match=$1
    shift
    for e; do [[ "$e" == "$match" ]] && return 0; done
    return 1
}

is_linux () {
    [[ $(uname -a) =~ "Linux" ]]
}

declare -a minikube_opts
minikube_opts[0]="--disk-size 80g"
if is_linux; then
    minikube_opts[1]="--vm-driver kvm2"
    minikube_opts[2]="--memory 8192"
else
    minikube_opts[1]="--vm-driver virtualbox"
    minikube_opts[2]="--memory 5120"
fi
readonly minikube_opts

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
    if [ ! -z "${OPS_MANAGER_HOST+x}" ] && [ ! -z "${OPS_MANAGER_PORT+x}" ]; then
        echo "Using external Ops Manager instance, no need to install"
        return
    fi

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

download_ego_script () {
    if [ -f "scripts/ego" ]; then
        return
    fi

    if [ ! -z "${GITHUB_TOKEN}" ]; then
        echo "To download ego you need to scpecify the 'GITHUB_TOKEN' environment variable"
        exit 1
    fi

    ego_location="https://raw.githubusercontent.com/10gen/mms/master/scripts/ops_manager/ego"
    curl -L "${ego_location}?token=${GITHUB_TOKEN}" -o scripts/ego && chmod +x scripts/ego
}

install_ops_manager_evg () {
    if ! evg list | grep -q -E "running|provisioning"; then
        evg spawn ubuntu1804-build
        sleep 3
        echo "Waiting for evergreen to finish provisioning the new host"
        while ! evg list | grep "running"; do sleep 5; done
    fi

    download_ego_script

    user=$(evg list | awk ' {print $3} ' | cut -d "@" 1)
    host=$(evg list | awk ' {print $3} ' | cut -d "@" 2)

    echo "ego is seeding itself into the new host"
    scripts/ego seed "${user}@${host}"

    echo "Deploying Ops Manager"
    ssh "${user}@${host}" ego scenario_install_package

    OPS_MANAGER_HOST=${host}
    OPS_MANAGER_PORT=9080 # TODO: find the actual port

    # Still need to wait for a bit for Ops Manager to really start
    sleep 10
}

get_ops_manager_exposed() {
    if ! is_om_managed_externally; then
        OPS_MANAGER_PORT="$(kubectl -n operator-testing get services/mongodb-enterprise-ops-manager-0 -o jsonpath='{.spec.ports[0].nodePort}')"
        OPS_MANAGER_HOST=$(kubectl cluster-info | grep -i "is running at" | grep master | awk '{ print $NF}' | cut -d "/" -f 3 | cut -d ":" -f 1)
    fi

    echo "http://${OPS_MANAGER_HOST}:${OPS_MANAGER_PORT}"
}

setup_minikube() {
    echo "Setting up minikube"
    if ! minikube -p "${MINIKUBE_CONTEXT}" status | grep -q "Running" ; then
        echo "Minikube is not running, starting it now"
        # If in Linux and you get this message from minikube:
        #  -> Requested operation is not valid: network 'minikube-net' is not active
        # Just do
        # virsh net-start minikube-nte
        minikube -p "${MINIKUBE_CONTEXT}" start "${minikube_opts}"
    fi
    eval "$(minikube -p ${MINIKUBE_CONTEXT} docker-env)"
}

minikube_cache_image () {
    # 'minikube cache' for an existing image takes about .7 seconds to complete, that's why I first check that the
    # image is not already present in the cache.
    if ! docker images | grep "$1" | grep "$2" > /dev/null; then minikube -p "${MINIKUBE_CONTEXT}" cache add "$1:$2"; fi
}

create_namespace () {
    if kubectl get "namespace/${NAMESPACE}" > /dev/null; then
        kubectl delete ns "${NAMESPACE}"

        echo "Waiting for ${NAMESPACE} to terminate"
        while kubectl get "ns/${NAMESPACE}" > /dev/null; do sleep 3; done
    fi

    echo "Creating namespace ${NAMESPACE}"
    kubectl create ns "${NAMESPACE}"
}

build_images () {
    echo "Building operator & database images"
    make -C docker/mongodb-enterprise-database build IMAGE_DIR=local || rollback "failed building database image"
    make -C docker/mongodb-enterprise-operator build IMAGE_DIR=local || rollback "failed building operator image"
}

get_ops_manager_config_env () {
    if is_om_managed_externally ; then
        envfile=".redo-ops-manager-env"
        if ! ssh "ubuntu@${OPS_MANAGER_HOST}" cat "~/${envfile}" > /dev/null; then
            echo "Configuring host"
            docker/mongodb-enterprise-ops-manager/scripts/configure-ops-manager.py \
                "http://${OPS_MANAGER_HOST}:${OPS_MANAGER_PORT}" "${envfile}"
            scp "${envfile}" "ubuntu@${OPS_MANAGER_HOST}:${envfile}"
        fi

        eval $(ssh "ubuntu@${OPS_MANAGER_HOST}" cat "~/${envfile}")
    else
        echo "Waiting for Ops Manager to finish configuring itself"
        while ! kubectl -n operator-testing exec mongodb-enterprise-ops-manager-0 cat /opt/mongodb/mms/env/.ops-manager-env > /dev/null ; do
            sleep 1
        done
        eval "$(kubectl -n operator-testing exec mongodb-enterprise-ops-manager-0 cat /opt/mongodb/mms/env/.ops-manager-env)"

        # om_host is not set correctly from "inside" ops-manager running in kubernetes so we patch it here.
        export OM_HOST="http://ops-manager-internal.operator-testing.svc.cluster.local:8080"
    fi
}

expose_ops_manager_minikube() {
    echo "Exposing Ops Manager"
    kubectl -n operator-testing expose pod mongodb-enterprise-ops-manager-0 --port 8080 --type NodePort
}

is_om_managed_externally () {
    [ ! -z "${OPS_MANAGER_HOST+x}" ] && [ ! -z "${OPS_MANAGER_PORT+x}" ]
}

wait_ops_manager_ready () {
    if is_om_managed_externally ; then
        echo "Ops Manager is installed in http://${OPS_MANAGER_HOST}:${OPS_MANAGER_PORT}"

        # TODO: wait for OM to be accessible
    else

        echo "OPS_MANAGER_HOST or OPS_MANAGER_PORT is not set, looking for OM in minikube"
        if ! kubectl -n operator-testing get pod/mongodb-enterprise-ops-manager-0 | grep -q "1/1"; then
            echo "It is now time to configure the operator, we'll have to wait until Ops Manager is ready for connections."
            while ! kubectl -n operator-testing get pod/mongodb-enterprise-ops-manager-0 | grep -q "1/1"; do sleep 5; done
        fi
    fi
}

configure_operator () {
    wait_ops_manager_ready
    get_ops_manager_config_env

    echo "Configuring Operator (project and credentials)"
    if ! kubectl --namespace "${NAMESPACE}" get secret/my-credentials; then
        kubectl --namespace "${NAMESPACE}" create secret generic my-credentials \
                --from-literal=user="${OM_USER:=admin}" \
                --from-literal=publicApiKey="${OM_API_KEY}"
    fi
    kubectl --namespace "${NAMESPACE}" describe secret my-credentials

    if ! kubectl --namespace "${NAMESPACE}" get configmap my-project; then
        kubectl --namespace "${NAMESPACE}" create configmap my-project \
                --from-literal=projectName="${NAMESPACE}" \
                --from-literal=baseUrl="${OM_HOST}"
    fi
    kubectl --namespace "${NAMESPACE}" describe configmap my-project
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

wait_operator_to_be_running () {
    while ! kubectl -n "${NAMESPACE}" get pods | grep mongodb-enterprise-operator > /dev/null ; do sleep 1; done
}

restart_operator() {
    build_images

    if ! kubectl -n "${NAMESPACE}" get configmap/my-project  || ! kubectl -n "${NAMESPACE}" get secrets/my-credentials ; then
        echo "Configuring operator"
        configure_operator
    fi
    # deleting operator
    OPERATOR_POD_NAME=$(kubectl -n "${NAMESPACE}" get pods | grep mongodb-enterprise-operator | grep "Running" | awk '{ print $1 }')
    kubectl -n "${NAMESPACE}" delete "pod/${OPERATOR_POD_NAME}"
}

NAMESPACE="${NAMESPACE_PREFIX}-$(cat .redo-last-namespace)"
echo "Trying to reuse namespace ${NAMESPACE}"

setup_minikube

echo "Caching images locally if needed"
minikube_cache_image "debian" "jessie-slim"
minikube_cache_image "debian" "9.5-slim"

if kubectl get "ns/${NAMESPACE}" > /dev/null && kubectl -n "${NAMESPACE}" get deployment/mongodb-enterprise-operator > /dev/null; then
    # the namespace and the operator is installed
    echo "Rebuilding the operator in place"
    restart_operator
else
    build_images
    NAMESPACE="$(use_new_namespace)"
    echo "Unable to use existing namespace, creating a new one ${NAMESPACE}"
    create_namespace

    install_ops_manager
    install_operator
    configure_operator
fi

echo "You can visit Ops Manager UI at $(get_ops_manager_exposed)"

echo "Waiting for operator to reach Running state"
wait_operator_to_be_running

log_operator () {
    while ! kubectl -n "${NAMESPACE}" get pods | grep mongodb-enterprise-operator | grep -q "Running"; do sleep 1; done

    OPERATOR_POD_NAME=$(kubectl -n "${NAMESPACE}" get pods | grep mongodb-enterprise-operator | grep "Running" | awk '{ print $1 }')
    kubectl -n "${NAMESPACE}" logs "${OPERATOR_POD_NAME}" -f &
    # send logging report to background and stay in foreground until pod is killed?
}

if contains "--watch" "$@"; then
    log_operator
    inotifywait -e close_write,moved_to,create -m docker/mongodb-enterprise-operator/content/ |
        while read -r directory events filename; do
            if [ "$filename" = "mongodb-enterprise-operator" ]; then
                restart_operator
                log_operator
            fi
        done
    echo "Exiting!"
else
    log_operator
    while kubectl -n "${NAMESPACE}" get pod "${OPERATOR_POD_NAME}" > /dev/null ; do sleep 1; done
fi
