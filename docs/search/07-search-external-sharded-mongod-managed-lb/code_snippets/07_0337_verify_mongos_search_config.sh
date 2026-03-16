#!/usr/bin/env bash
# Verify that mongos has correct search parameters configured
#
# mongos is the entry point for sharded cluster queries, including search queries.
# It needs search parameters to coordinate search requests across shards.

echo "Verifying mongos search configuration..."

mongos_pod="${MDB_EXTERNAL_MONGOS_NAME}-0"

mongot_host=$(kubectl exec "${mongos_pod}" -n "${MDB_NS}" --context "${K8S_CTX}" \
  -c mongodb-enterprise-database -- \
  mongosh --quiet --eval "db.adminCommand({getParameter: 1, mongotHost: 1}).mongotHost" 2>/dev/null || echo "")

echo "  mongotHost: ${mongot_host:-NOT SET}"

search_mgmt=$(kubectl exec "${mongos_pod}" -n "${MDB_NS}" --context "${K8S_CTX}" \
  -c mongodb-enterprise-database -- \
  mongosh --quiet --eval "db.adminCommand({getParameter: 1, searchIndexManagementHostAndPort: 1}).searchIndexManagementHostAndPort" 2>/dev/null || echo "")

echo "  searchIndexManagementHostAndPort: ${search_mgmt:-NOT SET}"

tls_mode=$(kubectl exec "${mongos_pod}" -n "${MDB_NS}" --context "${K8S_CTX}" \
  -c mongodb-enterprise-database -- \
  mongosh --quiet --eval "db.adminCommand({getParameter: 1, searchTLSMode: 1}).searchTLSMode" 2>/dev/null || echo "")

echo "  searchTLSMode: ${tls_mode:-NOT SET}"

use_grpc=$(kubectl exec "${mongos_pod}" -n "${MDB_NS}" --context "${K8S_CTX}" \
  -c mongodb-enterprise-database -- \
  mongosh --quiet --eval "db.adminCommand({getParameter: 1, useGrpcForSearch: 1}).useGrpcForSearch" 2>/dev/null || echo "")

echo "  useGrpcForSearch: ${use_grpc:-NOT SET}"

echo ""
if [[ -n "${mongot_host}" ]] && [[ "${tls_mode}" == "requireTLS" ]]; then
  echo "✓ mongos search configuration is correct"
else
  echo "⚠ mongos search configuration may need review"
  echo "  Verify the MongoDB sharded cluster was created with mongos search parameters."
fi
