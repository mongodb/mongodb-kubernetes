echo "Creating a \$search index and a \$vectorSearch index..."
echo "Index creation itself exercises the 12_0400 wiring: the mongod that receives"
echo "createSearchIndex forwards it to searchIndexManagementHostAndPort -- its own"
echo "cluster's local proxy -- and the metadata then replicates to every mongot."

kubectl exec --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" mongodb-tools-pod -- \
  mongosh --quiet "${MDB_CONNECTION_STRING}" \
    --eval "use sample_search" \
    --eval 'db.movies.createSearchIndex("default", { mappings: { dynamic: true } });' \
    --eval 'db.movies.createSearchIndex("vector_index", "vectorSearch", { fields: [ { type: "vector", path: "vec", numDimensions: 8, similarity: "cosine" } ] });'

echo "Waiting for both indexes to reach READY (index metadata + initial sync on every mongot)..."
ready=0
for i in $(seq 1 30); do
  ready=$(kubectl exec --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" mongodb-tools-pod -- \
    mongosh --quiet "${MDB_CONNECTION_STRING}" \
      --eval "use sample_search" \
      --eval 'db.movies.getSearchIndexes().filter(i => i.status == "READY").length' | tail -1)
  if [[ "${ready}" == "2" ]]; then
    break
  fi
  echo "  ${ready:-0}/2 indexes READY, retrying in 10s..."
  sleep 10
done

kubectl exec --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" mongodb-tools-pod -- \
  mongosh --quiet "${MDB_CONNECTION_STRING}" \
    --eval "use sample_search" \
    --eval 'db.movies.getSearchIndexes().map(i => ({name: i.name, type: i.type, status: i.status, queryable: i.queryable}))'

if [[ "${ready}" != "2" ]]; then
  echo "error: the indexes did not reach READY within 5 minutes -- stop here."
  echo "Inspect the index statuses above and each cluster's mongot pod logs before continuing."
  exit 1
fi
echo "[ok] both search indexes READY"
