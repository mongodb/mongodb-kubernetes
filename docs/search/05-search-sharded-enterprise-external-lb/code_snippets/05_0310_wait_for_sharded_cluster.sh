# Wait for sharded cluster to reach Running phase
echo "Waiting for MongoDB sharded cluster to reach Running phase..."
kubectl wait --context "${K8S_CTX}" -n "${MDB_NS}" \
  --for=jsonpath='{.status.phase}'=Running \
  mongodb/${MDB_RESOURCE_NAME} \
  --timeout=900s

echo "MongoDB sharded cluster is running"
kubectl get --context "${K8S_CTX}" -n "${MDB_NS}" mongodb/${MDB_RESOURCE_NAME}

