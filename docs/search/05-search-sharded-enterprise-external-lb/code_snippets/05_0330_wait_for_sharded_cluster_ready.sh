# Wait for sharded cluster to be fully ready after Search configuration
echo "Waiting for MongoDB sharded cluster to be fully configured with Search..."
kubectl wait --context "${K8S_CTX}" -n "${MDB_NS}" \
  --for=jsonpath='{.status.phase}'=Running \
  mongodb/${MDB_RESOURCE_NAME} \
  --timeout=600s

echo "MongoDB sharded cluster is ready with Search configuration"

