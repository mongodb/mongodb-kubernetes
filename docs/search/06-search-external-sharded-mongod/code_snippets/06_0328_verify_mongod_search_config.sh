# Verify that each shard's mongod has the correct search parameters configured
# including TLS and authentication settings
#
# For external MongoDB source, the search config is applied during initial
# cluster creation in 06_0310_create_external_mongodb_sharded_cluster.sh.
# This script verifies that the configuration was applied correctly.

echo "Verifying mongod search configuration for each shard..."
echo "Mongot replicas per shard: ${MDB_MONGOT_REPLICAS:-1}"

for i in $(seq 0 $((MDB_SHARD_COUNT - 1))); do
  shard_name="${MDB_EXTERNAL_CLUSTER_NAME}-${i}"
  pod_name="${shard_name}-0"

  echo; echo "=== Shard ${i} (${shard_name}) ==="
  echo "Pod: ${pod_name}"

  # Get the mongod configuration
  config=$(kubectl exec --context "${K8S_CTX}" -n "${MDB_NS}" ${pod_name} -- cat /data/automation-mongod.conf 2>/dev/null || echo "Failed to get config")

  # Extract and display search-related parameters
  echo "Search parameters:"
  echo "${config}" | grep -E "mongotHost|searchIndexManagementHostAndPort|searchTLSMode|skipAuthentication|useGrpcForSearch" || echo "  No search parameters found"

  # Verify TLS mode is set to requireTLS
  if echo "${config}" | grep -q "searchTLSMode.*requireTLS"; then
    echo "  ✓ searchTLSMode: requireTLS"
  else
    echo "  ✗ searchTLSMode is not set to requireTLS"
  fi

  # Verify authentication is enabled (skipAuthentication should be false)
  if echo "${config}" | grep -q "skipAuthenticationToSearchIndexManagementServer.*false"; then
    echo "  ✓ skipAuthenticationToSearchIndexManagementServer: false"
  else
    echo "  ✗ skipAuthenticationToSearchIndexManagementServer is not false"
  fi

  if echo "${config}" | grep -q "skipAuthenticationToMongot.*false"; then
    echo "  ✓ skipAuthenticationToMongot: false"
  else
    echo "  ✗ skipAuthenticationToMongot is not false"
  fi

  # Verify useGrpcForSearch is true
  if echo "${config}" | grep -q "useGrpcForSearch.*true"; then
    echo "  ✓ useGrpcForSearch: true"
  else
    echo "  ✗ useGrpcForSearch is not true"
  fi

  # Show mongot StatefulSet replicas
  sts_name="${MDB_SEARCH_RESOURCE_NAME}-mongot-${shard_name}"
  echo "Mongot StatefulSet replicas:"
  kubectl --context "${K8S_CTX}" -n "${MDB_NS}" get sts/${sts_name} -o jsonpath='{.spec.replicas}' 2>/dev/null && echo "" || echo "  StatefulSet not found"
done

echo; echo "Verification complete"
