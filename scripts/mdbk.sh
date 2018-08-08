#!/bin/bash

# mdbk.sh
#
# Manages MondoDB Enterprise Kubernetes set-up in different environments
#
# Usage:
#   ./mdbk.sh --openshift: Creates an Openshift cluster, configures Helm, installs the MongoDB-enterprise-operator
#   ./mdbk.sh --openshift --undeploy: Deletes an existing Openshift cluster
#
#   ./mdbk.sh --minikube: Creates a Minikube cluster and installs the MongoDB-enterprise-operator
#   ./mdbk.sh --minikube --undeploy: Deletes an existing Minikube cluster
#
#   ./mdbk.sh --helm: Installs and configures helm into a Kubernetes cluster (e.g., pre-existing EKS)
#   ./mdbk.sh --operator: Uses the local Kubectl context to install/reinstall the Operator and Ops Manager
#   ./mdbk.sh --operator --undeploy: Removes the Operator and Ops Manager containers from the Kubernetes cluster
#
readonly DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )/../docker" && pwd )"
set -o nounset
set -o errexit

# Define the project's namespace in Kubernetes (equates to project name in Openshift)
readonly TILLER_NAMESPACE="kube-system"
readonly PROJECT_NAMESPACE="mongodb"
readonly RESOURCES_NAMESPACE="${PROJECT_NAMESPACE}-resources"

# Array contains function; based on https://stackoverflow.com/questions/3685970/check-if-a-bash-array-contains-a-value
contains() {
    local e match=$1
    shift
    for e; do [[ "$e" == "$match" ]] && return 0; done
    return 1
}

# Determine the execution environment
OPENSHIFT="--openshift"
MINIKUBE="--minikube"
KUBE_ENV=""
if contains "${OPENSHIFT}" "$@"; then
    KUBE_ENV="${OPENSHIFT}"
elif contains "${MINIKUBE}" "$@"; then
    KUBE_ENV="${MINIKUBE}"
fi


#
# MongoDB Operator requirements
#
create_namespaces() {
    kubectl create namespace "${PROJECT_NAMESPACE}" || true
    kubectl create namespace "${RESOURCES_NAMESPACE}" || true
    kubectl create namespace "${TILLER_NAMESPACE}" || true
}
install_helm() {
    # Ensure the namespaces exist
    create_namespaces

    # Init Helm/Tiller
    kubectl create serviceaccount --namespace "${TILLER_NAMESPACE}" tiller
    kubectl create clusterrolebinding tiller-cluster-rule --clusterrole=cluster-admin --serviceaccount "${TILLER_NAMESPACE}:tiller"
    helm init --tiller-namespace "${TILLER_NAMESPACE}" --service-account tiller # TODO(mihaibojin): --tiller-namespace might not be required
    sleep 5 # Wait a bit to give tiller a chance to get deployed

    # TODO(mihaibojin): retest the script in Openshift and determine if the below can be removed
    #if [[ "${KUBE_ENV}" == "${OPENSHIFT}" ]]; then
    #    helm init --tiller-namespace "${TILLER_NAMESPACE}"
    #    oc process -f https://github.com/openshift/origin/raw/master/examples/helm/tiller-template.yaml -p TILLER_NAMESPACE="${TILLER_NAMESPACE}" -p HELM_VERSION=v2.9.0 | oc create -f -
    #    oc rollout status deployment tiller
    #    oc policy add-role-to-user edit "system:serviceaccount:${TILLER_NAMESPACE}:tiller"
    #    oc create clusterrolebinding tiller-binding --clusterrole=cluster-admin --serviceaccount "${TILLER_NAMESPACE}:tiller"
    #else
}
purge_operator() {
    if helm list --tiller-namespace "${TILLER_NAMESPACE}" mongodb-enterprise | grep -q 'mongodb-enterprise' >/dev/null 2>&1; then
        helm del --tiller-namespace "${TILLER_NAMESPACE}" --purge mongodb-enterprise
    fi
}
install_operator() {
    purge_operator
    sleep 3

    # Allow installs using a custom AWS ECS repository
    if [[ ! -z "${AWS_IMAGE_REPO+set_if_undef}" ]] && [[ ! -z "${CLUSTER_NAME+set_if_undef}" ]]; then
        helm install  --tiller-namespace "${TILLER_NAMESPACE}" --namespace "${PROJECT_NAMESPACE}" --name mongodb-enterprise "${DIR}/../public/helm_chart" -f "${DIR}/../public/helm_chart/values.yaml" \
            --set registry.host="${AWS_IMAGE_REPO}" \
            --set registry.repo="${CLUSTER_NAME}"
    else
        helm install --tiller-namespace "${TILLER_NAMESPACE}" --namespace "${PROJECT_NAMESPACE}" --name mongodb-enterprise "${DIR}/../public/helm_chart" -f "${DIR}/../public/helm_chart/values.yaml"
    fi

    # Print pod status
    echo
    echo "Waiting until the operator pod is running..."
    while ! kubectl --namespace "${PROJECT_NAMESPACE}" get pods | grep 'mongodb-enterprise-operator' | grep 'Running' >/dev/null; do sleep 1; done

    # Wait for Ops Manager to start
    if [[ "${KUBE_ENV}" == "${OPENSHIFT}" ]]; then
        await_ops_manager "openshift"
    else
        await_ops_manager "minikube"
    fi

    echo "Ops Manager is available at: $(get_om_host)"
    [ -x "$(command -v open)" ] && open "$(get_om_host)"
}

#
# Openshift
#
openshift_up() {
    # Init an Openshift cluster
    echo "Starting an Openshift cluster..."
    oc cluster up --version=v3.9.0
    oc login -u system:admin
    
    # Create a user for the deployment (same as namespace)
    oc adm policy add-role-to-user admin "${PROJECT_NAMESPACE}"
}
openshift_down() {
    echo "Shutting down the Openshift cluster..."
    oc cluster down
    # Nothing else to do after shutting down
    exit 0
}


#
# Minikube
#
minikube_up() {
    echo "Starting a Minikube cluster..."
    minikube start --memory=5120
    
    # Configure DNS
    minikube addons enable coredns
    (minikube addons disable kube-dns && kubectl delete --namespace=kube-system deployment kube-dns) || true

    # Use Docker using minikube's context
    eval "$(minikube docker-env)"
}
minikube_down() {
    echo "Shutting down the Minikube cluster..."
    minikube delete
    # Nothing else to do after shutting down
    exit 0
}


#
# Generic Ops Manager
#
configure_project() {
    # retrieve the environment vars
    eval "$(kubectl --namespace "${PROJECT_NAMESPACE}" exec mongodb-enterprise-ops-manager-0 cat /opt/mongodb/mms/.ops-manager-env)"

    # Configure the Kubernetes project
    cat <<EOF | kubectl --namespace "${PROJECT_NAMESPACE}" apply -f -
---
apiVersion: v1
kind: ConfigMap
metadata:
    name: my-project
    namespace: ${PROJECT_NAMESPACE}
data:
    projectId: ${OM_PROJECT_ID}
    baseUrl: ${OM_HOST}
EOF

    # Configure the Kubernetes credentials for Ops Manager
    kubectl --namespace "${PROJECT_NAMESPACE}" create secret generic my-credentials --from-literal=user="${OM_USER}" --from-literal=publicApiKey="${OM_API_KEY}"
}
await_ops_manager() {
    # If running in Openshift, expose a route
    if contains "--openshift" "$@"; then
        oc expose service ops-manager
    fi

    # Wait for Ops manager to start
    INTERIM_OM_HOST=$(get_om_host)
	echo "Waiting for Ops Manager (${INTERIM_OM_HOST}) to start..."
	while ! curl -qsI "${INTERIM_OM_HOST}/user" | grep '200 OK' >/dev/null; do printf . ; sleep 10; done
	echo "Waiting for Ops Manager global owner registration..."
	while ! kubectl --namespace "${PROJECT_NAMESPACE}" exec mongodb-enterprise-ops-manager-0 -- test -f /opt/mongodb/mms/.ops-manager-env; do printf . ; sleep 5; done

    # After Ops Manager has started, retrieve the environment vars and continue
    configure_project
}

get_om_host() {
    # Expose a route to the Ops Manager service
    if kubectl get route ops-manager >/dev/null 2>&1; then
        # If a route is defined (Openshift-specific), use it to find the exposed IP
        local OC_OM_HOST
        OC_OM_HOST="http://$(kubectl get route ops-manager -o jsonpath='{.spec.host}'):80/"
        echo "${OC_OM_HOST%/}"

    else
        local KUBE_CLUSTER KUBE_IP OPS_MANAGER_PORT

        if [[ ! -z "${CLUSTER+set_if_undef}" ]]; then
            # A CLUSTER env var exists, use it to determine the host
            KUBE_CLUSTER=$( kubectl config view -o jsonpath='{.clusters[?(@.name == "'"${CLUSTER}"'")].cluster.server}' )
        else
            # Assume we are running in Minikube
            KUBE_CLUSTER=$( kubectl config view -o jsonpath='{.clusters[?(@.name == "minikube")].cluster.server}' )
        fi

        # Stop if the cluster could not be determined (running in an unsupported one perhaps!?)
        if [[ -z "${KUBE_CLUSTER}" ]]; then
            echo "Could not determine the current cluster IP! You will need to expose the ops-manager service manually..."
            echo
            exit 1
        fi

        KUBE_IP="${KUBE_CLUSTER%:*}"
        KUBE_IP="${KUBE_IP#http*//}"
        OPS_MANAGER_PORT=$(kubectl --namespace "${PROJECT_NAMESPACE}" get -o jsonpath="{.spec.ports[0].nodePort}" services ops-manager)
        echo "http://${KUBE_IP}:${OPS_MANAGER_PORT}"
    fi
}


#
# Main
#

# Deploy an Openshift cluster
DID_NOT_RUN=true
if [[ "${KUBE_ENV}" == "${OPENSHIFT}" ]]; then
    unset DID_NOT_RUN
    if contains "--undeploy" "$@"; then
        openshift_down
    else
        openshift_up
        install_helm
        install_operator
    fi
elif [[ "${KUBE_ENV}" == "${MINIKUBE}" ]]; then
    unset DID_NOT_RUN
    if contains "--undeploy" "$@"; then
        minikube_down
    else
        minikube_up
        install_helm
        install_operator
    fi
fi

# Install helm
if contains "--helm" "$@"; then
    # Only if not already installed via ${KUBE_ENV}_up
    if [[ "${KUBE_ENV}" == "" ]]; then
        install_helm
    fi
fi

# Install the operator
if contains "--operator" "$@"; then
    if contains "--undeploy" "$@"; then
        purge_operator
        echo
        echo "You might also need to purge PVCs:"
        kubectl --namespace "${PROJECT_NAMESPACE}" get pvc
        echo
        echo "kubectl -n \"${PROJECT_NAMESPACE}\" delete pvc mongodb-mms-config-mongodb-enterprise-ops-manager-0 mongodb-mms-data-mongodb-enterprise-ops-manager-0"
        echo "kubectl -n \"${PROJECT_NAMESPACE}\" get pvc"
        echo
    else
        install_operator
    fi
fi

if contains "--configure" "$@"; then
    configure_project
fi

# Fail if nothing was executed
if [ -z "${DID_NOT_RUN-true}" ]; then
    echo "Nothing was run!"
    echo "Args: '$*'"
    echo
    exit 2
fi
