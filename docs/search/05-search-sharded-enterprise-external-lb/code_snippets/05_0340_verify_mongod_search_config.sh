# Verify that each shard's mongod has the correct search parameters configured
echo "Verifying mongod search configuration for each shard..."

for i in $(seq 0 $((MDB_SHARD_COUNT - 1))); do
  shard_name="${MDB_RESOURCE_NAME}-${i}"
  pod_name="${shard_name}-0"
  
  echo; echo "=== Shard ${i} (${shard_name}) ==="
  echo "Pod: ${pod_name}"
  
  # Get the mongod configuration
  config=$(kubectl exec --context "${K8S_CTX}" -n "${MDB_NS}" ${pod_name} -- cat /data/automation-mongod.conf 2>/dev/null || echo "Failed to get config")
  
  # Extract and display search-related parameters
  echo "Search parameters:"
  echo "${config}" | grep -E "mongotHost|searchIndexManagementHostAndPort" || echo "  No search parameters found"
done

echo; echo "Verification complete"

