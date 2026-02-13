# Wait for the external MongoDB sharded cluster to be ready
#
# This waits for the MongoDB resource to reach the Running state

echo "Waiting for MongoDB Sharded Cluster '${MDB_EXTERNAL_CLUSTER_NAME}' to be ready..."

# Wait for the MongoDB resource to be in Running state
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" wait --for=jsonpath='{.status.phase}'=Running \
  mongodb "${MDB_EXTERNAL_CLUSTER_NAME}" --timeout=900s

# Verify all pods are running
echo "Verifying all pods are running..."

# Wait for mongos pods
for ((i = 0; i < MDB_MONGOS_COUNT; i++)); do
  kubectl --context "${K8S_CTX}" -n "${MDB_NS}" wait --for=condition=Ready \
    pod "${MDB_EXTERNAL_CLUSTER_NAME}-mongos-${i}" --timeout=300s
done

# Wait for config server pods
for ((i = 0; i < MDB_CONFIG_SERVER_COUNT; i++)); do
  kubectl --context "${K8S_CTX}" -n "${MDB_NS}" wait --for=condition=Ready \
    pod "${MDB_EXTERNAL_CLUSTER_NAME}-config-${i}" --timeout=300s
done

# Wait for shard pods
for ((shard = 0; shard < MDB_SHARD_COUNT; shard++)); do
  for ((member = 0; member < MDB_MONGODS_PER_SHARD; member++)); do
    kubectl --context "${K8S_CTX}" -n "${MDB_NS}" wait --for=condition=Ready \
      pod "${MDB_EXTERNAL_CLUSTER_NAME}-${shard}-${member}" --timeout=300s
  done
done

echo "MongoDB Sharded Cluster '${MDB_EXTERNAL_CLUSTER_NAME}' is ready"

# Display cluster status
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" get mongodb "${MDB_EXTERNAL_CLUSTER_NAME}"
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" get pods -l app="${MDB_EXTERNAL_CLUSTER_NAME}"

