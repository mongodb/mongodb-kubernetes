#!/usr/bin/env bash
# Wait for search indexes to be ready
#
# Search indexes need time to build and sync data from MongoDB to mongot.
# This script polls the index status until all indexes are READY.
#
# ============================================================================
# INDEX STATUS PROGRESSION
# ============================================================================
# Typical status flow: PENDING → BUILDING → READY
# Time depends on:
# - Data size (sample_mflix.movies is ~23k documents, takes 1-3 minutes)
# - mongot resources (CPU/memory)
# - Number of shards (data is distributed across shards)
# ============================================================================
# DEPENDS ON:
#   - 07_0345_create_search_index.sh (text index must be created)
#   - 07_0346_create_vector_search_index.sh (vector index, optional)
# ============================================================================

echo "Waiting for search indexes to be ready..."
echo "This may take several minutes depending on data size..."
echo ""

# Connection string for user operations
# authMechanism=SCRAM-SHA-256 is required for MongoDB 8.2+ which only enables SCRAM-SHA-256
user_conn="mongodb://mdb-user:${MDB_USER_PASSWORD}@${MDB_EXTERNAL_CLUSTER_NAME}-mongos-0.${MDB_EXTERNAL_CLUSTER_NAME}-svc.${MDB_NS}.svc.cluster.local:27017/?tls=true&tlsCAFile=/tls/ca-pem&authSource=admin&authMechanism=SCRAM-SHA-256"

timeout=300  # 5 minutes
interval=10
elapsed=0

while [[ $elapsed -lt $timeout ]]; do
  text_status=$(kubectl exec mongodb-tools -n "${MDB_NS}" --context "${K8S_CTX}" -- \
    mongosh "${user_conn}" --quiet --eval '
      const indexes = db.getSiblingDB("sample_mflix").movies.aggregate([
        { $listSearchIndexes: { name: "default" } }
      ]).toArray();
      if (indexes.length > 0) {
        print(indexes[0].status);
      } else {
        print("NOT_FOUND");
      }
    ' 2>/dev/null || echo "ERROR")

  vector_status=$(kubectl exec mongodb-tools -n "${MDB_NS}" --context "${K8S_CTX}" -- \
    mongosh "${user_conn}" --quiet --eval '
      const indexes = db.getSiblingDB("sample_mflix").embedded_movies.aggregate([
        { $listSearchIndexes: { name: "vector_index" } }
      ]).toArray();
      if (indexes.length > 0) {
        print(indexes[0].status);
      } else {
        print("NOT_FOUND");
      }
    ' 2>/dev/null || echo "SKIPPED")

  echo "  Text index: ${text_status} | Vector index: ${vector_status} (${elapsed}s/${timeout}s)"

  # Check if text index is ready (vector is optional)
  if [[ "$text_status" == "READY" ]]; then
    echo ""
    echo "✓ Text search index is READY"
    if [[ "$vector_status" == "READY" ]]; then
      echo "✓ Vector search index is READY"
    elif [[ "$vector_status" != "NOT_FOUND" ]] && [[ "$vector_status" != "SKIPPED" ]]; then
      echo "⚠ Vector search index still building: ${vector_status}"
    fi
    exit 0
  fi

  sleep $interval
  elapsed=$((elapsed + interval))
done

echo ""
echo "⚠ Timeout waiting for search indexes"
echo "The indexes may still be building. You can check status manually:"
echo "  kubectl exec mongodb-tools -n ${MDB_NS} -- mongosh '${user_conn}' --eval 'db.getSiblingDB(\"sample_mflix\").movies.aggregate([{\$listSearchIndexes: {}}])'"
