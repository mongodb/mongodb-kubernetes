echo "Executing vector search query..."
echo ""

user_conn="${MDB_USER_CONNECTION_STRING:-${MDB_CONNECTION_STRING}}"

mdb_script=$(cat <<'MONGOSH'
use sample_mflix;

const sample = db.embedded_movies.findOne(
  { plot_embedding_voyage_3_large: { $exists: true } },
  { plot_embedding_voyage_3_large: 1, title: 1 }
);

if (!sample || !sample.plot_embedding_voyage_3_large) {
  print("ASSERTION FAILED: no embedded vector found in embedded_movies");
  quit(1);
}

const results = db.embedded_movies.aggregate([
  {
    $vectorSearch: {
      index: "vector_index",
      path: "plot_embedding_voyage_3_large",
      queryVector: sample.plot_embedding_voyage_3_large,
      numCandidates: 50,
      limit: 5
    }
  },
  {
    $project: {
      _id: 0,
      title: 1,
      year: 1,
      plot: 1,
      score: { $meta: "vectorSearchScore" }
    }
  }
]).toArray();
printjson(results);
print("Result count: " + results.length);
if (results.length === 0) {
  print("ASSERTION FAILED: vector search query returned no documents");
  quit(1);
}
MONGOSH
)

kubectl exec mongodb-tools \
  -n "${MDB_NS}" \
  --context "${K8S_CTX}" \
  -- /bin/bash -eu -c "$(cat <<EOF
echo '${mdb_script}' > /tmp/mdb_script.js
mongosh --quiet "${user_conn}" < /tmp/mdb_script.js
EOF
)"

echo ""
echo "Vector search query executed successfully"
