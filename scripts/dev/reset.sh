#!/usr/bin/env bash

set -Eeou pipefail

source scripts/dev/read_context.sh
source scripts/funcs/kubernetes
source scripts/funcs/printing
source scripts/funcs/multicluster

DELETE_CRD=${DELETE_CRD:-"true"}

red_color="\e[31m"
reset_color="\e[0m"

kubectl_cmd() {
  msg=$(timeout 5s kubectl "$@" 2>&1)
  error_code=$?
  if [[ ${error_code} -ne 0 ]]; then
      echo -e "${red_color}"
      echo "kubectl $*"
      echo -e "${msg}${reset_color}"
      exit ${error_code}
  fi
}

reset_context() {
  context=$1
  namespace=$2
  if [[ "${context}" == "" ]]; then
    echo "context cannot be empty"
    exit 1
  fi

  set +e

  helm uninstall --kube-context="${context}" mongodb-enterprise-operator || true &
  helm uninstall --kube-context="${context}" mongodb-enterprise-operator-multi-cluster || true &

  # Cleans the namespace. Note, that fine-grained cleanup is performed instead of just deleting the namespace as it takes
  # considerably less time
  title "Cleaning Kubernetes resources in context: ${context}"

  ensure_namespace "${namespace}"

  kubectl_cmd delete --context "${context}" mdb --all -n "${namespace}"
  kubectl_cmd delete --context "${context}" mdbu --all -n "${namespace}"
  kubectl_cmd delete --context "${context}" mdbmc --all -n "${namespace}"

  # Hack: remove the statefulset for backup daemon first - otherwise it may get stuck on removal if AppDB is removed first
  kubectl_cmd delete --context "${context}" "$(kubectl_cmd get sts -o name -n "${namespace}" | grep "backup-daemon")" 2>/dev/null

  # shellcheck disable=SC2016,SC2086
  timeout "30s" bash -c \
    'while [[ $(kubectl -n '${namespace}' get pods | grep "backup-daemon-0" | wc -l) -gt 0 ]]; do sleep 1; done' ||
    echo "Warning: failed to remove backup daemon statefulset"

  kubectl_cmd delete --context "${context}" sts --all -n "${namespace}"
  kubectl_cmd delete --context "${context}" deploymentsx --all -n "${namespace}"
  kubectl_cmd delete --context "${context}" services --all -n "${namespace}"
  kubectl_cmd delete --context "${context}" opsmanager --all -n "${namespace}"

  # shellcheck disable=SC2046
  for csr in $(kubectl_cmd get csr -o name | grep "${namespace}"); do
    kubectl_cmd delete --context "${context}" "${csr}"
  done

  kubectl_cmd delete --context "${context}" secrets --all -n "${namespace}"
  kubectl_cmd delete --context "${context}" svc --all -n "${namespace}"
  kubectl_cmd delete --context "${context}" configmaps --all -n "${namespace}"
  kubectl_cmd delete --context "${context}" validatingwebhookconfigurations/mdbpolicy.mongodb.com --ignore-not-found=true

  # certificates and issuers may not be installed
  kubectl_cmd delete --context "${context}" certificates --all -n "${namespace}"
  kubectl_cmd delete --context "${context}" issuers --all -n "${namespace}"
  kubectl_cmd delete --context "${context}" pvc --all -n "${namespace}"

  kubectl_cmd delete --context "${context}" catalogsources --all -n "${namespace}"
  kubectl_cmd delete --context "${context}" subscriptions --all -n "${namespace}"
  kubectl_cmd delete --context "${context}" clusterserviceversions --all -n "${namespace}"

  if [[ "${DELETE_CRD}" == "true" ]]; then
    kubectl_cmd delete --context "${context}" crd mongodb.mongodb.com
    kubectl_cmd delete --context "${context}" crd mongodbmulti.mongodb.com
    kubectl_cmd delete --context "${context}" crd mongodbmulticluster.mongodb.com
    kubectl_cmd delete --context "${context}" crd mongodbusers.mongodb.com
    kubectl_cmd delete --context "${context}" crd opsmanagers.mongodb.com
  fi

  kubectl_cmd delete --context "${context}"

  # shellcheck disable=SC2046
  kubectl_cmd delete --context "${context}" -n "${namespace}" $(kubectl_cmd get serviceaccounts --context "${context}" -n "${namespace}" -o name | grep -v default)
  # shellcheck disable=SC2046
  kubectl_cmd delete --context "${context}" -n "${namespace}" $(kubectl_cmd get rolebindings --context "${context}" -n "${namespace}" -o name | grep mongodb)
  # shellcheck disable=SC2046
  kubectl_cmd delete --context "${context}" -n "${namespace}" $(kubectl_cmd get roles --context "${context}" -n "${namespace}" -o name | grep mongodb) || true

  echo "Finished resetting context ${context}/${namespace}"

  set -e
}

# shellcheck disable=SC2154
if [[ "${kube_environment_name}" == "multi" ]]; then
  # reset central cluster
  central_cluster_namespaces=$(get_test_namespaces "${CENTRAL_CLUSTER}")
  echo "${CENTRAL_CLUSTER}: resetting namespaces: ${central_cluster_namespaces}"
  for ns in ${central_cluster_namespaces}; do
    reset_context "${CENTRAL_CLUSTER}" "${ns}" 2>&1 | prepend "${CENTRAL_CLUSTER}:${ns}" &
  done

# we are in our static cluster, lets skip!
  if [[ "${central_cluster}" != "e2e.operator.mongokubernetes.com" ]]; then
    # reset member clusters
    for member_cluster in ${MEMBER_CLUSTERS}; do
      member_cluster_namespaces=$(get_test_namespaces "${member_cluster}")
      echo "${member_cluster}: resetting namespaces: ${member_cluster_namespaces}"
      for ns in ${member_cluster_namespaces}; do
        reset_context "${member_cluster}" "${ns}" 2>&1 | prepend "${member_cluster}:${ns}" &
      done
    done
  fi
  echo "Waiting for background jobs to complete..."
  wait
else
  reset_context "$(kubectl_cmd config current-context)" "${NAMESPACE}"
fi
