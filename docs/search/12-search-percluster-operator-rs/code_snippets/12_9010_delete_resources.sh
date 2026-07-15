echo "WARNING: This will delete the MongoDBSearch resource and the per-cluster Search"
echo "operator release from every member cluster. It does NOT touch the source"
echo "MongoDBMultiCluster, the central operator, or namespaces (those belong to ra-07/ra-02)."
echo ""

read -rp "Are you sure you want to continue? (yes/no): " confirm

if [[ "${confirm}" != "yes" ]]; then
  echo "Cleanup cancelled."
else
  for ctx in "${K8S_CLUSTER_0_CONTEXT_NAME}" "${K8S_CLUSTER_1_CONTEXT_NAME}" "${K8S_CLUSTER_2_CONTEXT_NAME}"; do
    kubectl delete mongodbsearch "${SEARCH_RESOURCE_NAME}" -n "${MDB_NAMESPACE}" --context "${ctx}" --ignore-not-found --wait=false
    helm uninstall "${SEARCH_OPERATOR_RELEASE_NAME}" --namespace "${MDB_NAMESPACE}" --kube-context "${ctx}" 2>/dev/null || true
    echo "  [ok] cleanup initiated in ${ctx}"
  done

  echo ""
  echo "Resources may take a few minutes to fully terminate."
fi
