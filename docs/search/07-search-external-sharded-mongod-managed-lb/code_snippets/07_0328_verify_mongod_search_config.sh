#!/usr/bin/env bash
# Verify that each shard's mongod has correct search parameters
#
# For managed Envoy LB, each shard's mongod should have:
# - mongotHost: pointing to the operator-managed Envoy proxy Service (port 27029)
# - searchIndexManagementHostAndPort: same as mongotHost
# - searchTLSMode: requireTLS
# - useGrpcForSearch: true
#
# This confirms the mongod → Envoy → mongot traffic path is configured.

echo "Verifying mongod search configuration for each shard..."
echo ""

all_correct=true

for ((shard = 0; shard < MDB_SHARD_COUNT; shard++)); do
  shard_name="${MDB_EXTERNAL_CLUSTER_NAME}-${shard}"
  pod_name="${shard_name}-0"
  expected_proxy="${MDB_SEARCH_RESOURCE_NAME}-search-0-${shard_name}-proxy-svc"
  expected_port="${ENVOY_PROXY_PORT:-27029}"
  
  echo "Checking shard: ${shard_name}"
  
  mongot_host=$(kubectl exec "${pod_name}" -n "${MDB_NS}" --context "${K8S_CTX}" \
    -c mongodb-enterprise-database -- \
    mongosh --quiet --eval "db.adminCommand({getParameter: 1, mongotHost: 1}).mongotHost" 2>/dev/null || echo "")
  
  if [[ "${mongot_host}" == *"${expected_proxy}"* ]] && [[ "${mongot_host}" == *"${expected_port}"* ]]; then
    echo "  ✓ mongotHost: ${mongot_host}"
  else
    echo "  ✗ mongotHost: ${mongot_host:-NOT SET}"
    echo "    Expected: ${expected_proxy}.${MDB_NS}.svc.cluster.local:${expected_port}"
    all_correct=false
  fi
  
  tls_mode=$(kubectl exec "${pod_name}" -n "${MDB_NS}" --context "${K8S_CTX}" \
    -c mongodb-enterprise-database -- \
    mongosh --quiet --eval "db.adminCommand({getParameter: 1, searchTLSMode: 1}).searchTLSMode" 2>/dev/null || echo "")
  
  if [[ "${tls_mode}" == "requireTLS" ]]; then
    echo "  ✓ searchTLSMode: ${tls_mode}"
  else
    echo "  ⚠ searchTLSMode: ${tls_mode:-NOT SET} (expected: requireTLS)"
  fi
  
  use_grpc=$(kubectl exec "${pod_name}" -n "${MDB_NS}" --context "${K8S_CTX}" \
    -c mongodb-enterprise-database -- \
    mongosh --quiet --eval "db.adminCommand({getParameter: 1, useGrpcForSearch: 1}).useGrpcForSearch" 2>/dev/null || echo "")
  
  if [[ "${use_grpc}" == "true" ]]; then
    echo "  ✓ useGrpcForSearch: ${use_grpc}"
  else
    echo "  ⚠ useGrpcForSearch: ${use_grpc:-NOT SET} (expected: true)"
  fi
  
  echo ""
done

if [[ "${all_correct}" == "true" ]]; then
  echo "✓ All shards have correct mongod search configuration"
else
  echo "✗ Some shards have incorrect configuration"
  echo "  Make sure the MongoDB cluster was created with search parameters"
  echo "  pointing to the operator-managed Envoy proxy Services."
  exit 1
fi

