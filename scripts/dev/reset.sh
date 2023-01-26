#!/usr/bin/env bash

set -Eeou pipefail

source scripts/dev/read_context.sh
source scripts/funcs/kubernetes
source scripts/funcs/printing
source scripts/funcs/multicluster

reset_context() {
  context=$1
  if [[ "${context}" == "" ]]; then
    echo "context cannot be empty"
    exit 1
  fi

  # Cleans the namespace. Note, that fine-grained cleanup is performed instead of just deleting the namespace as it takes
  # considerably less time
  title "Cleaning Kubernetes resources in context: ${context}"

  ensure_namespace "${NAMESPACE}"

  kubectl delete --context "${context}" mdb --all -n "${NAMESPACE}" || true
  kubectl delete --context "${context}" mdbu --all -n "${NAMESPACE}" || true
  kubectl delete --context "${context}" mdbm --all -n "${NAMESPACE}" || true

  # Hack: remove the statefulset for backup daemon first - otherwise it may get stuck on removal if AppDB is removed first
  kubectl delete --context "${context}" "$(kubectl get sts -o name -n "${NAMESPACE}" | grep "backup-daemon")" 2>/dev/null || true

  # shellcheck disable=SC2016,SC2086
  timeout "30s" bash -c \
            'while [[ $(kubectl -n '${NAMESPACE}' get pods | grep "backup-daemon-0" | wc -l) -gt 0 ]]; do sleep 1; done' \
             || echo "Warning: failed to remove backup daemon statefulset"

  kubectl delete --context "${context}" opsmanager --all -n "${NAMESPACE}" || true
  kubectl delete --context "${context}" pvc --all -n "${NAMESPACE}"

  helm uninstall mongodb-enterprise-operator || true

  # shellcheck disable=SC2046
  for csr in $(kubectl get csr -o name | grep "${NAMESPACE}"); do
      kubectl delete --context "${context}" "${csr}"
  done
  # note, that "kubectl delete --context "${context}" .. -all" always enables "--ignore-not-found=true" option so there's no need to tolerate
  # failures explicitly (" || true")
  kubectl delete --context "${context}" secrets --all -n "${NAMESPACE}"
  kubectl delete --context "${context}" svc --all -n "${NAMESPACE}"
  kubectl delete --context "${context}" configmaps --all -n "${NAMESPACE}"
  kubectl delete --context "${context}" sts --all -n "${NAMESPACE}"
  kubectl delete --context "${context}" validatingwebhookconfigurations/mdbpolicy.mongodb.com --ignore-not-found=true

  # certificates and issuers may not be installed
  kubectl delete --context "${context}" certificates --all -n "${NAMESPACE}" || true
  kubectl delete --context "${context}" issuers --all -n "${NAMESPACE}" || true

  kubectl delete --context "${context}" services --all -n "${NAMESPACE}" || true
  kubectl delete --context "${context}" deployments --all -n "${NAMESPACE}" || true
}

reset_multi_cluster_member_cluster() {
  member_cluster=$1
  title "Cleaning Kubernetes resources in member cluster: ${member_cluster}"

  kubectl delete --context "${context}" --context "${member_cluster}" svc --all -n "${NAMESPACE}"
  kubectl delete --context "${context}" --context "${member_cluster}" sts --all -n "${NAMESPACE}"
  kubectl delete --context "${context}" --context "${member_cluster}" secrets --all -n "${NAMESPACE}"
  kubectl delete --context "${context}" --context "${member_cluster}" configmaps --all -n "${NAMESPACE}"
  kubectl delete --context "${context}" --context "${member_cluster}" serviceaccounts --all -n "${NAMESPACE}"
}

# shellcheck disable=SC2154
if [[ "${kube_environment_name}" == "multi" ]]; then
  reset_context "${CENTRAL_CLUSTER}"
  for member_cluster in ${MEMBER_CLUSTERS}; do
    reset_multi_cluster_member_cluster "${member_cluster}" &
  done
  wait
else
  reset_context "$(kubectl config current-context)"
fi

if [[ "${kube_environment_name:-}" = "multi" && -n "${local:-}" ]]; then
  prepare_multi_cluster_e2e_run
fi