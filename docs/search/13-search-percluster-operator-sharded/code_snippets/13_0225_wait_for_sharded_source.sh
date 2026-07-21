echo "Waiting for the sharded source MongoDB to be ready..."
echo "This may take several minutes..."

kubectl wait --for=jsonpath='{.status.phase}'=Running \
  mongodb/"${MDB_RESOURCE_NAME}" \
  -n "${MDB_NAMESPACE}" \
  --context "${K8S_CLUSTER_0_CONTEXT_NAME}" \
  --timeout=900s

echo "[ok] Sharded source MongoDB is Running"

echo ""
echo "Source pods:"
kubectl get pods -n "${MDB_NAMESPACE}" --context "${K8S_CLUSTER_0_CONTEXT_NAME}" \
  -l "app=${MDB_RESOURCE_NAME}-shard" --no-headers
kubectl get pods -n "${MDB_NAMESPACE}" --context "${K8S_CLUSTER_0_CONTEXT_NAME}" \
  -l "app=${MDB_RESOURCE_NAME}-config" --no-headers
kubectl get pods -n "${MDB_NAMESPACE}" --context "${K8S_CLUSTER_0_CONTEXT_NAME}" \
  -l "app=${MDB_RESOURCE_NAME}-mongos" --no-headers
