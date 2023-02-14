#!/usr/bin/env bash

set -Eeou pipefail

source scripts/dev/read_context.sh
source scripts/funcs/kubernetes
source scripts/funcs/printing
source scripts/funcs/multicluster

reset_context() {
  context=$1
  namespace=$2
  if [[ "${context}" == "" ]]; then
    echo "context cannot be empty"
    exit 1
  fi

  # Cleans the namespace. Note, that fine-grained cleanup is performed instead of just deleting the namespace as it takes
  # considerably less time
  title "Cleaning Kubernetes resources in context: ${context}"

  ensure_namespace "${namespace}"

  kubectl delete --context "${context}" mdb --all -n "${namespace}" || true
  kubectl delete --context "${context}" mdbu --all -n "${namespace}" || true
  kubectl delete --context "${context}" mdbm --all -n "${namespace}" || true

  # Hack: remove the statefulset for backup daemon first - otherwise it may get stuck on removal if AppDB is removed first
  kubectl delete --context "${context}" "$(kubectl get sts -o name -n "${namespace}" | grep "backup-daemon")" 2>/dev/null || true

  # shellcheck disable=SC2016,SC2086
  timeout "30s" bash -c \
            'while [[ $(kubectl -n '${namespace}' get pods | grep "backup-daemon-0" | wc -l) -gt 0 ]]; do sleep 1; done' \
             || echo "Warning: failed to remove backup daemon statefulset"

  kubectl delete --context "${context}" sts --all -n "${namespace}"
  kubectl delete --context "${context}" deployments --all -n "${namespace}" || true
  kubectl delete --context "${context}" services --all -n "${namespace}" || true
  kubectl delete --context "${context}" opsmanager --all -n "${namespace}" || true

  # shellcheck disable=SC2046
  for csr in $(kubectl get csr -o name | grep "${namespace}"); do
      kubectl delete --context "${context}" "${csr}"
  done
  # note, that "kubectl delete --context "${context}" .. -all" always enables "--ignore-not-found=true" option so there's no need to tolerate
  # failures explicitly (" || true")
  kubectl delete --context "${context}" secrets --all -n "${namespace}"
  kubectl delete --context "${context}" svc --all -n "${namespace}"
  kubectl delete --context "${context}" configmaps --all -n "${namespace}"
  kubectl delete --context "${context}" validatingwebhookconfigurations/mdbpolicy.mongodb.com --ignore-not-found=true

  # certificates and issuers may not be installed
  kubectl delete --context "${context}" certificates --all -n "${namespace}" || true
  kubectl delete --context "${context}" issuers --all -n "${namespace}" || true
  kubectl delete --context "${context}" pvc --all -n "${namespace}"

  echo "Finished resetting context ${context}/${namespace}"
}

helm uninstall mongodb-enterprise-operator || true &

# shellcheck disable=SC2154
if [[ "${kube_environment_name}" == "multi" ]]; then
  # reset central cluster
  central_cluster_namespaces=$(get_test_namespaces "${CENTRAL_CLUSTER}")
  echo "${CENTRAL_CLUSTER}: resetting namespaces: ${central_cluster_namespaces}"
  for ns in ${central_cluster_namespaces}; do
    reset_context "${CENTRAL_CLUSTER}" "${ns}" 2>&1 | prepend "${CENTRAL_CLUSTER}:${ns}" &
  done

  # reset member clusters
  for member_cluster in ${MEMBER_CLUSTERS}; do
    member_cluster_namespaces=$(get_test_namespaces "${member_cluster}")
    echo "${member_cluster}: resetting namespaces: ${member_cluster_namespaces}"
    for ns in ${member_cluster_namespaces}; do
      reset_context "${member_cluster}" "${ns}" 2>&1 | prepend "${member_cluster}:${ns}" &
    done
  done
  echo "Waiting for background jobs to complete..."
  wait
else
  reset_context "$(kubectl config current-context)" "${NAMESPACE}"
fi
