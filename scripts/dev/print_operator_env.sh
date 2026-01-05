#!/usr/bin/env bash

set -Eeou pipefail

# CLOUDP-301133: Refactored to read from the operator-installation-config ConfigMap.
# This ensures the ConfigMap is the single source of truth for operator configuration,
# used by both deployed operators (via helm) and local development.
#
# Prerequisites: Run `make prepare-local-e2e` first to create the ConfigMap.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="${SCRIPT_DIR}/../.."

# Extract environment variables by reading helm values from the ConfigMap
# and rendering them through helm template
extract_env_vars_from_configmap() {
  # Read helm values from the ConfigMap and convert to helm --set arguments
  local helm_args=()
  while IFS='=' read -r key value; do
    helm_args+=("--set" "${key}=${value}")
  done < <(kubectl get configmap operator-installation-config -n "${NAMESPACE}" -o json \
    | jq -r '.data | to_entries[] | "\(.key)=\(.value)"')

  # Run helm template and extract env vars from the deployment
  # Filter out env vars that use valueFrom (they're set by k8s at runtime)
  helm template mongodb-kubernetes-operator "${PROJECT_DIR}/helm_chart" \
    "${helm_args[@]}" \
    --show-only templates/operator.yaml 2>/dev/null \
    | yq e '
      .spec.template.spec.containers[0].env[]
      | select(.value != null)
      | .name + "=\"" + .value + "\""
    ' -
}

# Print environment variables that are set at runtime by k8s (via valueFrom.fieldRef)
# but need to be explicitly set when running locally
print_runtime_env_vars() {
  echo "NAMESPACE=\"${NAMESPACE}\""
  echo "WATCH_NAMESPACE=\"${WATCH_NAMESPACE}\""
}

# Print local-only environment variables that are not part of the helm chart
print_local_only_env_vars() {
  [[ "${KUBECONFIG:-}" != "" ]] && echo "KUBECONFIG=\"${KUBECONFIG}\""
  [[ "${KUBE_CONFIG_PATH:-}" != "" ]] && echo "KUBE_CONFIG_PATH=\"${KUBE_CONFIG_PATH}\""
  [[ "${MDB_AGENT_DEBUG:-}" != "" ]] && echo "MDB_AGENT_DEBUG=\"${MDB_AGENT_DEBUG}\""
  [[ "${MDB_AGENT_DEBUG_IMAGE:-}" != "" ]] && echo "MDB_AGENT_DEBUG_IMAGE=\"${MDB_AGENT_DEBUG_IMAGE}\""
  [[ "${OM_DEBUG_HTTP:-}" != "" ]] && echo "OM_DEBUG_HTTP=\"${OM_DEBUG_HTTP}\""
  [[ "${OPS_MANAGER_MONITOR_APPDB:-}" != "" ]] && echo "OPS_MANAGER_MONITOR_APPDB=\"${OPS_MANAGER_MONITOR_APPDB}\""
  [[ "${MDB_OM_VERSION_MAPPING_PATH:-}" != "" ]] && echo "MDB_OM_VERSION_MAPPING_PATH=\"${MDB_OM_VERSION_MAPPING_PATH}\""
  [[ "${MDB_AGENT_VERSION:-}" != "" ]] && echo "MDB_AGENT_VERSION=\"${MDB_AGENT_VERSION}\""
  [[ "${MONGODB_AGENT_VERSION:-}" != "" ]] && echo "MONGODB_AGENT_VERSION=\"${MONGODB_AGENT_VERSION}\""
  true  # Ensure function returns 0 even if all conditions are false
}

print_operator_env() {
  extract_env_vars_from_configmap
  print_runtime_env_vars
  print_local_only_env_vars
}

print_operator_env
