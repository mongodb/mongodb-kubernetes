kubectl exec -i --context "${K8S_CTX}" -n "${MDB_NS}" mongodb-tools-pod -- \
  mongosh --quiet "${MDB_CONNECTION_STRING}" <<'EOF'
use sample_mflix;

const queryVector = db.embedded_movies.findOne(
  { plot_embedding_voyage_3_large: { $exists: true } },
  { plot_embedding_voyage_3_large: 1 }
).plot_embedding_voyage_3_large;

db.embedded_movies.aggregate([
  {
    $vectorSearch: {
      index: "vector_index",
      path: "plot_embedding_voyage_3_large",
      queryVector,
      numCandidates: 100,
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
]);
EOF
