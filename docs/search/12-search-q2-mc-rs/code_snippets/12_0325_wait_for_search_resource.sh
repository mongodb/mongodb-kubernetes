echo "Waiting for MongoDBSearch top-level status.phase=Running..."

kubectl wait --for=jsonpath='{.status.phase}'=Running \
  mongodbsearch/"${MDB_SEARCH_RESOURCE_NAME}" \
  -n "${MDB_NS}" \
  --context "${K8S_CENTRAL_CTX}" \
  --timeout=600s

echo "Waiting for ${MEMBER_CLUSTER_0_NAME} per-cluster status.phase=Running (spec §4.3)..."
kubectl wait --for=jsonpath='{.status.clusterStatusList.clusterStatuses[?(@.clusterName=="'"${MEMBER_CLUSTER_0_NAME}"'")].phase}'=Running \
  mongodbsearch/"${MDB_SEARCH_RESOURCE_NAME}" \
  -n "${MDB_NS}" \
  --context "${K8S_CENTRAL_CTX}" \
  --timeout=600s

echo "Waiting for ${MEMBER_CLUSTER_1_NAME} per-cluster status.phase=Running..."
kubectl wait --for=jsonpath='{.status.clusterStatusList.clusterStatuses[?(@.clusterName=="'"${MEMBER_CLUSTER_1_NAME}"'")].phase}'=Running \
  mongodbsearch/"${MDB_SEARCH_RESOURCE_NAME}" \
  -n "${MDB_NS}" \
  --context "${K8S_CENTRAL_CTX}" \
  --timeout=600s

echo "[ok] MongoDBSearch is Running across all member clusters"
echo ""
kubectl get mongodbsearch "${MDB_SEARCH_RESOURCE_NAME}" -n "${MDB_NS}" --context "${K8S_CENTRAL_CTX}" -o yaml \
  | grep -A 80 '^status:' | head -80
