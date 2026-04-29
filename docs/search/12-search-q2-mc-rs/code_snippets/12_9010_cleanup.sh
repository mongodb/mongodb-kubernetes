echo "Deleting MongoDBSearch '${MDB_SEARCH_RESOURCE_NAME}' from central cluster..."
kubectl delete mongodbsearch "${MDB_SEARCH_RESOURCE_NAME}" \
  -n "${MDB_NS}" --context "${K8S_CENTRAL_CTX}" --ignore-not-found --wait=true

echo "Deleting namespace '${MDB_NS}' from each cluster (non-blocking)..."
kubectl delete namespace "${MDB_NS}" --context "${K8S_CENTRAL_CTX}"  --wait=false --ignore-not-found
kubectl delete namespace "${MDB_NS}" --context "${K8S_CLUSTER_0_CTX}" --wait=false --ignore-not-found
kubectl delete namespace "${MDB_NS}" --context "${K8S_CLUSTER_1_CTX}" --wait=false --ignore-not-found

echo "[ok] Cleanup initiated"
