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

# --- Shard 0 ---
echo "Checking shard: ${MDB_EXTERNAL_SHARD_0_NAME}"

mongot_host=$(kubectl exec "${MDB_EXTERNAL_SHARD_0_POD}" -n "${MDB_NS}" --context "${K8S_CTX}" \
  -c mongodb-enterprise-database -- \
  mongosh --quiet --eval "db.adminCommand({getParameter: 1, mongotHost: 1}).mongotHost" 2>/dev/null || echo "")

if [[ "${mongot_host}" == *"${MDB_PROXY_SVC_SHARD_0}"* ]] && [[ "${mongot_host}" == *"27029"* ]]; then
  echo "  ✓ mongotHost: ${mongot_host}"
else
  echo "  ✗ mongotHost: ${mongot_host:-NOT SET}"
  echo "    Expected: ${MDB_PROXY_HOST_SHARD_0}"
  all_correct=false
fi

tls_mode=$(kubectl exec "${MDB_EXTERNAL_SHARD_0_POD}" -n "${MDB_NS}" --context "${K8S_CTX}" \
  -c mongodb-enterprise-database -- \
  mongosh --quiet --eval "db.adminCommand({getParameter: 1, searchTLSMode: 1}).searchTLSMode" 2>/dev/null || echo "")

if [[ "${tls_mode}" == "requireTLS" ]]; then
  echo "  ✓ searchTLSMode: ${tls_mode}"
else
  echo "  ⚠ searchTLSMode: ${tls_mode:-NOT SET} (expected: requireTLS)"
fi

use_grpc=$(kubectl exec "${MDB_EXTERNAL_SHARD_0_POD}" -n "${MDB_NS}" --context "${K8S_CTX}" \
  -c mongodb-enterprise-database -- \
  mongosh --quiet --eval "db.adminCommand({getParameter: 1, useGrpcForSearch: 1}).useGrpcForSearch" 2>/dev/null || echo "")

if [[ "${use_grpc}" == "true" ]]; then
  echo "  ✓ useGrpcForSearch: ${use_grpc}"
else
  echo "  ⚠ useGrpcForSearch: ${use_grpc:-NOT SET} (expected: true)"
fi

echo ""

# --- Shard 1 ---
echo "Checking shard: ${MDB_EXTERNAL_SHARD_1_NAME}"

mongot_host=$(kubectl exec "${MDB_EXTERNAL_SHARD_1_POD}" -n "${MDB_NS}" --context "${K8S_CTX}" \
  -c mongodb-enterprise-database -- \
  mongosh --quiet --eval "db.adminCommand({getParameter: 1, mongotHost: 1}).mongotHost" 2>/dev/null || echo "")

if [[ "${mongot_host}" == *"${MDB_PROXY_SVC_SHARD_1}"* ]] && [[ "${mongot_host}" == *"27029"* ]]; then
  echo "  ✓ mongotHost: ${mongot_host}"
else
  echo "  ✗ mongotHost: ${mongot_host:-NOT SET}"
  echo "    Expected: ${MDB_PROXY_HOST_SHARD_1}"
  all_correct=false
fi

tls_mode=$(kubectl exec "${MDB_EXTERNAL_SHARD_1_POD}" -n "${MDB_NS}" --context "${K8S_CTX}" \
  -c mongodb-enterprise-database -- \
  mongosh --quiet --eval "db.adminCommand({getParameter: 1, searchTLSMode: 1}).searchTLSMode" 2>/dev/null || echo "")

if [[ "${tls_mode}" == "requireTLS" ]]; then
  echo "  ✓ searchTLSMode: ${tls_mode}"
else
  echo "  ⚠ searchTLSMode: ${tls_mode:-NOT SET} (expected: requireTLS)"
fi

use_grpc=$(kubectl exec "${MDB_EXTERNAL_SHARD_1_POD}" -n "${MDB_NS}" --context "${K8S_CTX}" \
  -c mongodb-enterprise-database -- \
  mongosh --quiet --eval "db.adminCommand({getParameter: 1, useGrpcForSearch: 1}).useGrpcForSearch" 2>/dev/null || echo "")

if [[ "${use_grpc}" == "true" ]]; then
  echo "  ✓ useGrpcForSearch: ${use_grpc}"
else
  echo "  ⚠ useGrpcForSearch: ${use_grpc:-NOT SET} (expected: true)"
fi

echo ""

if [[ "${all_correct}" == "true" ]]; then
  echo "✓ All shards have correct mongod search configuration"
else
  echo "✗ Some shards have incorrect configuration"
  echo "  Make sure the MongoDB cluster was created with search parameters"
  echo "  pointing to the operator-managed Envoy proxy Services."
  exit 1
fi
