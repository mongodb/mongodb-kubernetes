echo; echo "MongoDB Sharded Cluster resource"
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" get mongodb/${MDB_RESOURCE_NAME}

echo; echo "MongoDBSearch resource"
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" get mdbs/${MDB_RESOURCE_NAME}

echo; echo "Per-shard mongot Services"
for i in $(seq 0 $((MDB_SHARD_COUNT - 1))); do
  shard_name="${MDB_RESOURCE_NAME}-${i}"
  svc_name="${MDB_RESOURCE_NAME}-mongot-${shard_name}-svc"
  echo "  Shard ${i}: ${svc_name}"
  kubectl --context "${K8S_CTX}" -n "${MDB_NS}" get svc/${svc_name} 2>/dev/null || echo "    Service not found"
done

echo; echo "Per-shard mongot StatefulSets"
for i in $(seq 0 $((MDB_SHARD_COUNT - 1))); do
  shard_name="${MDB_RESOURCE_NAME}-${i}"
  sts_name="${MDB_RESOURCE_NAME}-mongot-${shard_name}"
  echo "  Shard ${i}: ${sts_name}"
  kubectl --context "${K8S_CTX}" -n "${MDB_NS}" get sts/${sts_name} 2>/dev/null || echo "    StatefulSet not found"
done

echo; echo "Per-shard mongot Pods"
for i in $(seq 0 $((MDB_SHARD_COUNT - 1))); do
  shard_name="${MDB_RESOURCE_NAME}-${i}"
  sts_name="${MDB_RESOURCE_NAME}-mongot-${shard_name}"
  echo "  Shard ${i} mongot pods:"
  kubectl --context "${K8S_CTX}" -n "${MDB_NS}" get pods -l app=${sts_name} 2>/dev/null || echo "    No pods found"
done

echo; echo "All pods in namespace ${MDB_NS}"
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" get pods
