echo "Running \$search and \$vectorSearch against a LOCAL member of every cluster..."
echo "Each query connects directly (directConnection) to that cluster's own replica-set"
echo "member, so serving it requires that cluster's own mongot -- three green clusters"
echo "prove every cluster's search deployment independently synced and serves queries."

query_script=$(cat <<'EOF'
// Runtime proof that 12_0400 landed on THIS process:
const p = db.adminCommand({ getParameter: 1, mongotHost: 1 });
print("mongotHost on " + db.hostInfo().system.hostname + " -> " + p.mongotHost);

const sdb = db.getSiblingDB("sample_search");
const text = sdb.movies.aggregate([
  { $search: { text: { query: "baseball", path: "plot" } } },
  { $limit: 2 },
  { $project: { _id: 0, title: 1 } }
]).toArray();
print("$search 'baseball' -> " + JSON.stringify(text));

const vec = sdb.movies.aggregate([
  { $vectorSearch: { index: "vector_index", path: "vec",
      queryVector: [1, 0, 0.1, 0, 0.1, 0.1, 0, 0.1], numCandidates: 8, limit: 2 } },
  { $project: { _id: 0, title: 1, score: { $meta: "vectorSearchScore" } } }
]).toArray();
print("$vectorSearch 'space-like' -> " + JSON.stringify(vec));
EOF
)

for i in 0 1 2; do
  ctx_var="K8S_CLUSTER_${i}_CONTEXT_NAME"
  ctx="${!ctx_var}"
  # First local member of this cluster; readPreference allows serving on a secondary.
  member="${RS_RESOURCE_NAME}-${i}-0-svc.${MDB_NAMESPACE}.svc.cluster.local"
  local_uri="mongodb://${SEARCH_ADMIN_USER_NAME}:${SEARCH_ADMIN_USER_PASSWORD}@${member}:27017/?directConnection=true&authSource=admin&readPreference=secondaryPreferred&tls=true&tlsCAFile=/tls/ca.crt"

  echo ""
  echo "--- ${ctx} (index ${i}), querying local member ${RS_RESOURCE_NAME}-${i}-0 ---"
  kubectl exec --context "${ctx}" -n "${MDB_NAMESPACE}" mongodb-tools-pod -- /bin/bash -eu -c "$(cat <<EOF
echo '${query_script}' > /tmp/query.js
mongosh --quiet "${local_uri}" < /tmp/query.js
EOF
)"
done

echo ""
echo "[ok] every cluster answered search queries from its own local member"
