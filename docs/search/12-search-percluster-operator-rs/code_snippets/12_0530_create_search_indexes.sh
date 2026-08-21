: "${K8S_CLUSTER_0_CONTEXT_NAME:?not set -- source the env files first (see README, Environment section)}"
: "${MDB_CONNECTION_STRING:?not set -- source the env files first (see README, Environment section)}"
: "${MDB_NAMESPACE:?not set -- source the env files first (see README, Environment section)}"

echo "Creating a \$search index and a \$vectorSearch index..."

create_script=$(cat <<'EOF'
db = db.getSiblingDB("sample_search");
// Safe to re-run: creation is skipped when an index of that name already exists.
if (!db.movies.getSearchIndexes().some(ix => ix.name == "default")) {
  db.movies.createSearchIndex("default", { mappings: { dynamic: true } });
  print("created $search index 'default'");
}
if (!db.movies.getSearchIndexes().some(ix => ix.name == "vector_index")) {
  db.movies.createSearchIndex("vector_index", "vectorSearch",
    { fields: [ { type: "vector", path: "vec", numDimensions: 8, similarity: "cosine" } ] });
  print("created $vectorSearch index 'vector_index'");
}
EOF
)

kubectl exec --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" \
  mongodb-tools-pod -- /bin/bash -eu -c "$(cat <<EOF
echo '${create_script}' > /tmp/create_indexes.js
mongosh --quiet "${MDB_CONNECTION_STRING}" --file /tmp/create_indexes.js
EOF
)"

echo "Waiting for both indexes to reach READY (index metadata + initial sync on every mongot)..."
ready=0
for _ in $(seq 1 30); do
  ready=$(kubectl exec --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" mongodb-tools-pod -- \
    mongosh --quiet "${MDB_CONNECTION_STRING}" \
      --eval 'db.getSiblingDB("sample_search").movies.getSearchIndexes().filter(i => i.status == "READY").length' | tail -1)
  if [[ "${ready}" == "2" ]]; then
    break
  fi
  echo "  ${ready:-0}/2 indexes READY, retrying in 10s..."
  sleep 10
done

kubectl exec --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" mongodb-tools-pod -- \
  mongosh --quiet "${MDB_CONNECTION_STRING}" \
    --eval 'db.getSiblingDB("sample_search").movies.getSearchIndexes().map(i => ({name: i.name, status: i.status, queryable: i.queryable}))'

if [[ "${ready}" != "2" ]]; then
  echo "error: the indexes did not reach READY within 5 minutes -- stop here."
  echo "Inspect the index statuses above and each cluster's mongot pod logs before continuing."
  exit 1
fi
echo "[ok] both search indexes READY"
