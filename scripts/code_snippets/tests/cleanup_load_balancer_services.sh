#!/usr/bin/env bash
# Deletes Kubernetes LoadBalancer Services across all GKE member clusters so
# GKE releases the GCP load-balancer resources (forwarding rules, target
# pools, health checks) they provision. Must run BEFORE cluster teardown:
# once the clusters are gone, those GCP resources are orphaned.
#
# Best-effort: every context/namespace pair is attempted even when earlier
# ones fail (a missing or unreachable cluster must not shield the others);
# the aggregate status is returned at the end. Shared by both GKE
# multi-cluster snippet runners.

list_load_balancer_services() {
  local context="${1}"
  local namespace="${2}"

  kubectl get services --context "${context}" --namespace "${namespace}" --ignore-not-found \
    -o jsonpath='{range .items[?(@.spec.type=="LoadBalancer")]}{.metadata.name}{"\n"}{end}'
}

cleanup_load_balancer_services() {
  local context namespace services service remaining failed=0

  for context in "${K8S_CLUSTER_0_CONTEXT_NAME}" "${K8S_CLUSTER_1_CONTEXT_NAME}" "${K8S_CLUSTER_2_CONTEXT_NAME}"; do
    for namespace in "${OM_NAMESPACE}" "${MDB_NAMESPACE}"; do
      if ! services="$(list_load_balancer_services "${context}" "${namespace}")"; then
        echo "Failed to list LoadBalancer Services in ${namespace} on ${context}" >&2
        failed=1
        continue
      fi

      while IFS= read -r service; do
        if [ -z "${service}" ]; then
          continue
        fi

        echo "Deleting LoadBalancer Service ${service} in ${namespace} on ${context}"
        if ! kubectl delete service "${service}" --context "${context}" --namespace "${namespace}" \
          --wait=true --timeout=5m --ignore-not-found; then
          echo "Failed to delete LoadBalancer Service ${service} in ${namespace} on ${context}" >&2
          failed=1
        fi
      done <<< "${services}"

      if ! remaining="$(list_load_balancer_services "${context}" "${namespace}")"; then
        echo "Failed to verify LoadBalancer Services in ${namespace} on ${context}" >&2
        failed=1
        continue
      fi
      if [ -n "${remaining}" ]; then
        echo "LoadBalancer Services remain in ${namespace} on ${context}: ${remaining}" >&2
        failed=1
      fi
    done
  done

  return "${failed}"
}
