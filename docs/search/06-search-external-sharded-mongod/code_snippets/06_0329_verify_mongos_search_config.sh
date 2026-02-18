# Verify that mongos has the correct search parameters configured
# For external MongoDB source, the search config is applied during initial
# cluster creation in 06_0310_create_external_mongodb_sharded_cluster.sh.
#
# Expected configuration:
# - mongotHost: pointing to the first shard's mongot endpoint via Envoy proxy
# - searchIndexManagementHostAndPort: same as mongotHost
# - searchTLSMode: requireTLS (when TLS is enabled)
# - useGrpcForSearch: true

echo "Verifying mongos search configuration..."

mongos_pod="${MDB_EXTERNAL_CLUSTER_NAME}-mongos-0"

echo "=== Mongos (${mongos_pod}) ==="

# Get the mongos configuration
config=$(kubectl exec --context "${K8S_CTX}" -n "${MDB_NS}" "${mongos_pod}" -- cat /var/lib/mongodb-mms-automation/workspace/mongos-"${mongos_pod}".conf 2>/dev/null || echo "Failed to get config")

# Extract and display search-related parameters
echo "Search parameters from config file:"
echo "${config}" | grep -E "mongotHost|searchIndexManagementHostAndPort|searchTLSMode|useGrpcForSearch|skipAuthentication" || echo "  No search parameters found in config file"

# Verify parameters at runtime using getParameter through mongos
echo ""
echo "Verifying runtime parameters..."

kubectl exec -n "${MDB_NS}" --context "${K8S_CTX}" \
  mongodb-tools-pod -- env MDB_CONNECTION_STRING="${MDB_CONNECTION_STRING}" /bin/bash -eu -c "$(cat <<'EOF'
mongosh "${MDB_CONNECTION_STRING}" --quiet --eval '
  const params = ["mongotHost", "searchIndexManagementHostAndPort", "searchTLSMode", "useGrpcForSearch"];
  params.forEach(p => {
    try {
      const r = db.adminCommand({ getParameter: 1, [p]: 1 });
      print(p + ": " + r[p]);
    } catch (e) {
      print(p + ": ERROR - " + e.message);
    }
  });
'
EOF
)"

# Verify expected values
errors=0

if echo "${config}" | grep -q "mongotHost"; then
  echo "✓ mongotHost is configured"
else
  echo "✗ mongotHost is NOT configured"
  errors=$((errors + 1))
fi

if echo "${config}" | grep -q "searchIndexManagementHostAndPort"; then
  echo "✓ searchIndexManagementHostAndPort is configured"
else
  echo "✗ searchIndexManagementHostAndPort is NOT configured"
  errors=$((errors + 1))
fi

if echo "${config}" | grep -q "useGrpcForSearch.*true"; then
  echo "✓ useGrpcForSearch: true"
else
  echo "✗ useGrpcForSearch is not set to true"
  errors=$((errors + 1))
fi

if [[ "${MDB_TLS_ENABLED:-false}" == "true" ]]; then
  if echo "${config}" | grep -q "searchTLSMode.*requireTLS"; then
    echo "✓ searchTLSMode: requireTLS"
  else
    echo "✗ searchTLSMode is not set to requireTLS"
    errors=$((errors + 1))
  fi
fi

echo ""
if [[ ${errors} -eq 0 ]]; then
  echo "Mongos search configuration verification: PASSED"
else
  echo "Mongos search configuration verification: WARNING (${errors} issues found)"
  echo "Note: This is informational only - search may still work if runtime parameters are set correctly"
fi
