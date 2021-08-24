#!/usr/bin/env bash
set -Eeou pipefail

KUBECONFIG_SAVED="${KUBECONFIG}"
unset KUBECONFIG

# Make sure kubectl fails if no KUBECONFIG is set
if kubectl get nodes &> /dev/null; then
    echo "There's a kubectl context defined globally ($HOME/.kube/config exists)"
    exit 1
fi

KUBECONFIG="${KUBECONFIG_SAVED}"
export KUBECONFIG

source scripts/funcs/kubernetes

##
## Does some preliminary actions to prepare testing environment:
## - fixes AWS
## - installs cluster cleaner
## - starts Ops Manager if it's not started yet and if it's necessary
##

# cleanup_env fixes AWS problems if any
fix_env() {
    ns_count="$(kubectl get ns -o name | grep a- --count || true)"
    echo "Current number of test namespaces: $ns_count"

    # sometimes in kops cluster some nodes get this taint that makes nodes non-schedulable. Just going over all nodes and
    # trying to remove the taint is supposed to help
    echo "##### Fixing taints"
    for n in $(kubectl get nodes -o name); do
        kubectl taint nodes "${n}" NodeWithImpairedVolumes:NoSchedule- 2> /dev/null || true
    done

    echo "##### Removing FAILED Persistent Volumes if any"
    # Note, that these volumes will be removed from EBS eventually (during a couple of hours), most of all they are stuck
    # in attaching - this results in nodes getting taints "NoSchedule"
    for v in $(kubectl get pv -o=custom-columns=NAME:.metadata.name,status:.status.phase | grep Failed | awk '{ print $1 }'); do
        kubectl delete pv "${v}"
    done
}

deploy_cluster_cleaner() {
    local current_context
    current_context="$(kubectl config current-context)"
    local ops_manager_namespace="${1}"
    local context="${2:-$current_context}"
    local cleaner_namespace="cluster-cleaner"

    if ! kubectl --context "${context}" get "ns/${cleaner_namespace=}" &> /dev/null ; then
        kubectl --context "${context}" create namespace "${cleaner_namespace=}"
    fi

    if [[ -n "${ops_manager_namespace}" ]]; then
        kubectl --context "${context}" create namespace "${ops_manager_namespace}" || true
    fi

    helm template docker/cluster-cleaner \
         --set cleanerVersion=latest \
         --set namespace="${ops_manager_namespace}" \
         --set cleanerNamespace=cluster-cleaner \
         > cluster-cleaner.yaml
    kubectl --context "${context}" apply -f cluster-cleaner.yaml
    rm cluster-cleaner.yaml
}

ops_manager_namespace="${1:-}"
ops_manager_version="${2:-}"
node_port="${3:-}"

echo "Fixing AWS problems if any"
fix_env

echo "Deploying cluster-cleaner"

# shellcheck disable=SC2154
if [[ "${kube_environment_name}" = "multi" ]]; then
    deploy_cluster_cleaner "${ops_manager_namespace}" "${central_cluster}"
    for member_cluster in ${member_clusters}; do
        deploy_cluster_cleaner "${ops_manager_namespace}" "${member_cluster}"
    done
else
    deploy_cluster_cleaner "${ops_manager_namespace}"
fi

echo "Installing/Upgrading all CRDs"
# Both replace and apply are required. If there are no CRDs on this cluster
# (cluster is empty or fresh install), the `kubectl replace` command will fail,
# so we apply the CRDs. The next run the `kubectl replace` will succeed.
kubectl replace -f public/helm_chart/crds || kubectl apply -f public/helm_chart/crds

if [[ "${kube_environment_name}" = "multi" ]]; then
    kubectl apply --context "${central_cluster}" -f config/crd/bases/mongodb.com_mongodbmulti.yaml
fi

if [[ "${OM_EXTERNALLY_CONFIGURED:-}" != "true" ]] && [[ -n "${ops_manager_namespace}" ]]; then
    ensure_ops_manager_k8s "${ops_manager_namespace}" "${ops_manager_version}" "${node_port}" "${ecr_registry:-}" "${version_id:-}"
fi
