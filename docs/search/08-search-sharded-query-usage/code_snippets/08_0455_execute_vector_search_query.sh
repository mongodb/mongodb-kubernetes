echo "Executing vector search query..."
echo ""

user_conn="${MDB_USER_CONNECTION_STRING:-${MDB_CONNECTION_STRING}}"

kubectl exec -i mongodb-tools \
  -n "${MDB_NS}" \
  --context "${K8S_CTX}" \
  -- mongosh --quiet "${user_conn}" <<'MONGOSH'
use sample_mflix;

const sample = db.embedded_movies.findOne(
  { plot_embedding_voyage_3_large: { $exists: true } },
  { plot_embedding_voyage_3_large: 1, title: 1 }
);
if (!sample || !sample.plot_embedding_voyage_3_large) {
  throw new Error("no embedded vector found in embedded_movies");
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
  throw new Error("vector search query returned no documents");
}
MONGOSH

echo ""
echo "Vector search query executed successfully"
