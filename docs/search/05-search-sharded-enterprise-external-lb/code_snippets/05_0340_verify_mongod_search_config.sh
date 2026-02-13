# Verify that each shard's mongod has the correct search parameters configured
# including TLS and authentication settings
echo "Verifying mongod search configuration for each shard..."
echo "Mongot replicas per shard: ${MDB_MONGOT_REPLICAS:-1}"

for i in $(seq 0 $((MDB_SHARD_COUNT - 1))); do
  shard_name="${MDB_RESOURCE_NAME}-${i}"
  pod_name="${shard_name}-0"

  echo; echo "=== Shard ${i} (${shard_name}) ==="
  echo "Pod: ${pod_name}"

  # Get the mongod configuration
  config=$(kubectl exec --context "${K8S_CTX}" -n "${MDB_NS}" "${pod_name}" -- cat /data/automation-mongod.conf 2>/dev/null || echo "Failed to get config")

  # Extract and display search-related parameters
  echo "Search parameters:"
  echo "${config}" | grep -E "mongotHost|searchIndexManagementHostAndPort|searchTLSMode|skipAuthentication" || echo "  No search parameters found"

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

  # Show mongot StatefulSet replicas
  sts_name="${MDB_RESOURCE_NAME}-mongot-${shard_name}"
  echo "Mongot StatefulSet replicas:"
  kubectl --context "${K8S_CTX}" -n "${MDB_NS}" get sts/"${sts_name}" -o jsonpath='{.spec.replicas}' 2>/dev/null && echo "" || echo "  StatefulSet not found"
done

# Verify per-shard TLS secrets
echo; echo "=== Per-Shard TLS Secrets Verification ==="
all_secrets_exist=true
for i in $(seq 0 $((MDB_SHARD_COUNT - 1))); do
  shard_name="${MDB_RESOURCE_NAME}-${i}"

  # Check user-provided TLS secret (from cert-manager)
  # Pattern: {prefix}-{shardName}-search-cert
  source_secret="${MDB_SEARCH_TLS_CERT_PREFIX}-${shard_name}-search-cert"
  if kubectl --context "${K8S_CTX}" -n "${MDB_NS}" get secret "${source_secret}" &>/dev/null; then
    echo "  ✓ Source TLS secret exists: ${source_secret}"
  else
    echo "  ✗ Source TLS secret missing: ${source_secret}"
    all_secrets_exist=false
  fi

  # Check operator-managed TLS secret (combined cert+key)
  # Pattern: {shardName}-search-certificate-key
  operator_secret="${shard_name}-search-certificate-key"
  if kubectl --context "${K8S_CTX}" -n "${MDB_NS}" get secret "${operator_secret}" &>/dev/null; then
    echo "  ✓ Operator TLS secret exists: ${operator_secret}"
  else
    echo "  ✗ Operator TLS secret missing: ${operator_secret}"
    all_secrets_exist=false
  fi
done

if [ "$all_secrets_exist" = true ]; then
  echo; echo "✓ All per-shard TLS secrets exist"
else
  echo; echo "✗ Some per-shard TLS secrets are missing"
fi

echo; echo "Verification complete"
