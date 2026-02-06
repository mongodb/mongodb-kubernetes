# Wait for MongoDB sharded cluster to be fully ready after search configuration update
#
# After updating the MongoDB cluster with search configuration, the operator
# will roll out the changes to all mongod and mongos pods. This script waits
# for the cluster to reach Running phase again.

echo "Waiting for MongoDB sharded cluster to be fully configured with Search..."
kubectl wait --context "${K8S_CTX}" -n "${MDB_NS}" \
  --for=jsonpath='{.status.phase}'=Running \
  mongodb/${MDB_EXTERNAL_CLUSTER_NAME} \
  --timeout=600s

echo "✓ MongoDB sharded cluster is ready with Search configuration"

# Show the current status
kubectl get --context "${K8S_CTX}" -n "${MDB_NS}" mongodb/${MDB_EXTERNAL_CLUSTER_NAME}

