echo "Verifying per-cluster MongoDBSearch resources..."
echo "Each cluster's operator must have created ONLY its own index-suffixed resources."

verify_cluster() {
  local ctx=$1
  local idx=$2

  local sts_name="${SEARCH_RESOURCE_NAME}-search-${idx}"
  local svc_name="${SEARCH_RESOURCE_NAME}-search-${idx}-svc"
  local proxy_svc_name="${SEARCH_RESOURCE_NAME}-search-${idx}-proxy-svc"
  local envoy_deployment="${SEARCH_RESOURCE_NAME}-search-lb-${idx}"

  echo ""
  echo "--- ${ctx} (index ${idx}) ---"
  kubectl get mongodbsearch "${SEARCH_RESOURCE_NAME}" -n "${MDB_NAMESPACE}" --context "${ctx}" \
    -o jsonpath='phase={.status.phase}{"\n"}'

  kubectl get statefulset "${sts_name}" -n "${MDB_NAMESPACE}" --context "${ctx}"
  kubectl get service "${svc_name}" "${proxy_svc_name}" -n "${MDB_NAMESPACE}" --context "${ctx}"
  kubectl get deployment "${envoy_deployment}" -n "${MDB_NAMESPACE}" --context "${ctx}"

  echo "Confirming no foreign cluster's resources leaked into ${ctx}..."
  for foreign_idx in "${SEARCH_CLUSTER_0_INDEX}" "${SEARCH_CLUSTER_1_INDEX}" "${SEARCH_CLUSTER_2_INDEX}"; do
    [[ "${foreign_idx}" == "${idx}" ]] && continue
    if kubectl get statefulset "${SEARCH_RESOURCE_NAME}-search-${foreign_idx}" -n "${MDB_NAMESPACE}" --context "${ctx}" &>/dev/null; then
      echo "  [FAIL] found foreign StatefulSet for index ${foreign_idx} in ${ctx}" >&2
    else
      echo "  [ok] no StatefulSet for foreign index ${foreign_idx} in ${ctx}"
    fi
  done
}

verify_cluster "${K8S_CLUSTER_0_CONTEXT_NAME}" "${SEARCH_CLUSTER_0_INDEX}"
verify_cluster "${K8S_CLUSTER_1_CONTEXT_NAME}" "${SEARCH_CLUSTER_1_INDEX}"
verify_cluster "${K8S_CLUSTER_2_CONTEXT_NAME}" "${SEARCH_CLUSTER_2_INDEX}"
