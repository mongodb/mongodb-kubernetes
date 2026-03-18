#!/usr/bin/env bash
# Wait for the operator-managed MongoDB sharded cluster to be ready

echo "Waiting for MongoDB sharded cluster to be ready..."
echo "This may take several minutes..."

# Wait for MongoDB resource to reach Running phase
timeout=900  # 15 minutes
interval=10
elapsed=0

while [[ ${elapsed} -lt ${timeout} ]]; do
  phase=$(kubectl get mongodb "${MDB_RESOURCE_NAME}" \
    -n "${MDB_NS}" \
    --context "${K8S_CTX}" \
    -o jsonpath='{.status.phase}' 2>/dev/null || echo "Unknown")

  if [[ "${phase}" == "Running" ]]; then
    echo "✓ MongoDB sharded cluster is Running"
    break
  fi

  echo "  Current phase: ${phase} (${elapsed}s/${timeout}s)"
  sleep ${interval}
  elapsed=$((elapsed + interval))
done

if [[ ${elapsed} -ge ${timeout} ]]; then
  echo "ERROR: Timeout waiting for MongoDB cluster to be ready"
  kubectl describe mongodb "${MDB_RESOURCE_NAME}" -n "${MDB_NS}" --context "${K8S_CTX}"
  exit 1
fi

# Show cluster status
echo ""
echo "Cluster pods:"
kubectl get pods -n "${MDB_NS}" --context "${K8S_CTX}" -l "app=${MDB_RESOURCE_NAME}-shard" --no-headers
kubectl get pods -n "${MDB_NS}" --context "${K8S_CTX}" -l "app=${MDB_RESOURCE_NAME}-config" --no-headers
kubectl get pods -n "${MDB_NS}" --context "${K8S_CTX}" -l "app=${MDB_RESOURCE_NAME}-mongos" --no-headers

echo ""
echo "✓ Operator-managed MongoDB cluster is ready"
