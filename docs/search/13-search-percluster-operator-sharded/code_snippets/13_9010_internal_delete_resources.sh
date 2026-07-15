echo "WARNING: This will delete the MongoDBSearch resource, its per-cluster Search operator" \
  "in both clusters, AND the sharded source MongoDB deployed by this scenario in cluster 0."
echo ""

read -rp "Are you sure you want to continue? (yes/no): " confirm

if [[ "${confirm}" != "yes" ]]; then
  echo "Cleanup cancelled."
else
  for ctx in "${K8S_CLUSTER_0_CONTEXT_NAME}" "${K8S_CLUSTER_1_CONTEXT_NAME}"; do
    echo "Deleting MongoDBSearch and Search operator in cluster ${ctx}..."
    kubectl delete mongodbsearch "${MDBS_RESOURCE_NAME}" -n "${MDB_NAMESPACE}" --context "${ctx}" --wait=false --ignore-not-found
    helm uninstall "${SEARCH_OPERATOR_RELEASE_NAME}" --kube-context "${ctx}" --namespace "${MDB_NAMESPACE}" || true
  done

  echo "Deleting the sharded source MongoDB (cluster 0)..."
  kubectl delete mongodbuser "${MDB_RESOURCE_NAME}-search-sync-source" -n "${MDB_NAMESPACE}" --context "${K8S_CLUSTER_0_CONTEXT_NAME}" --ignore-not-found
  kubectl delete mongodb "${MDB_RESOURCE_NAME}" -n "${MDB_NAMESPACE}" --context "${K8S_CLUSTER_0_CONTEXT_NAME}" --wait=false --ignore-not-found

  echo ""
  echo "Deletion initiated. Resources may take a few minutes to fully terminate."
fi
