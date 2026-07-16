echo "Verifying each cluster's operator only manages its own index..."

for cluster_idx in "${MDBS_CLUSTER_0_INDEX}" "${MDBS_CLUSTER_1_INDEX}"; do
  if [[ "${cluster_idx}" == "${MDBS_CLUSTER_0_INDEX}" ]]; then
    ctx="${K8S_CLUSTER_0_CONTEXT_NAME}"
  else
    ctx="${K8S_CLUSTER_1_CONTEXT_NAME}"
  fi

  echo ""
  echo "--- Cluster ${ctx} (index ${cluster_idx}) ---"

  echo "MongoDBSearch status (independent per cluster, no cross-cluster aggregation):"
  kubectl get mongodbsearch "${MDBS_RESOURCE_NAME}" -n "${MDB_NAMESPACE}" --context "${ctx}"

  echo "mongot StatefulSets (expect only *-search-${cluster_idx}-* here):"
  kubectl get statefulset -n "${MDB_NAMESPACE}" --context "${ctx}" \
    -l "app.kubernetes.io/component=mongot"

  echo "Envoy Deployment for this cluster only (${MDBS_RESOURCE_NAME}-search-lb-${cluster_idx}):"
  kubectl get deployment "${MDBS_RESOURCE_NAME}-search-lb-${cluster_idx}" -n "${MDB_NAMESPACE}" --context "${ctx}"

  echo "SNI names this cluster's Envoy actually serves (should list only shard names, and only this cluster's segment):"
  kubectl get configmap "${MDBS_RESOURCE_NAME}-search-lb-${cluster_idx}-config" -n "${MDB_NAMESPACE}" --context "${ctx}" \
    -o jsonpath='{.data.lds\.json}' | jq -r '[.. | .server_names? // empty] | flatten | unique[]'

  other_idx="${MDBS_CLUSTER_1_INDEX}"
  [[ "${cluster_idx}" == "${MDBS_CLUSTER_1_INDEX}" ]] && other_idx="${MDBS_CLUSTER_0_INDEX}"
  foreign_segment="${MDBS_RESOURCE_NAME}-search-${other_idx}-"
  leaked=$(kubectl get configmap "${MDBS_RESOURCE_NAME}-search-lb-${cluster_idx}-config" -n "${MDB_NAMESPACE}" --context "${ctx}" \
    -o jsonpath='{.data.lds\.json}{.data.cds\.json}' | grep -c "${foreign_segment}" || true)
  if [[ "${leaked}" -gt 0 ]]; then
    echo "  [FAIL] this cluster's Envoy config references cluster ${other_idx}'s resources (${foreign_segment}*)" >&2
  else
    echo "  [ok] no references to cluster ${other_idx}'s resources"
  fi
done
