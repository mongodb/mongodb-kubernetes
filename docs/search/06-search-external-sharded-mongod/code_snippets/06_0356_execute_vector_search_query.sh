# Execute vector search queries through mongos
# This verifies that mongos can route vector search queries to mongot and aggregate results from all shards

echo "Executing vector search queries through mongos..."

echo "=== Test 1: Get a sample embedding to use as query vector ==="
sample_info=$(kubectl exec -n "${MDB_NS}" --context "${K8S_CTX}" \
  mongodb-tools-pod -- env MDB_CONNECTION_STRING="${MDB_CONNECTION_STRING}" /bin/bash -eu -c "$(cat <<'EOF'
mongosh "${MDB_CONNECTION_STRING}" --quiet --eval '
  const db_mflix = db.getSiblingDB("sample_mflix");

  // Find a movie with an embedding to use as query vector
  const sampleMovie = db_mflix.embedded_movies.findOne(
    { plot_embedding_voyage_3_large: { $exists: true } },
    { title: 1, plot: 1 }
  );

  if (sampleMovie) {
    print("SAMPLE_TITLE:" + sampleMovie.title);
    print("SAMPLE_PLOT:" + (sampleMovie.plot ? sampleMovie.plot.substring(0, 100) : "N/A"));
  } else {
    print("ERROR:No movie with embedding found");
  }
'
EOF
)" 2>/dev/null)

echo "${sample_info}" | grep -E "^SAMPLE_" | sed 's/^SAMPLE_/  /'

if echo "${sample_info}" | grep -q "^ERROR:"; then
  echo "ERROR: Could not find sample movie with embedding"
  exit 1
fi

echo ""
echo "=== Test 2: Vector search for similar movies ==="
result=$(kubectl exec -n "${MDB_NS}" --context "${K8S_CTX}" \
  mongodb-tools-pod -- env MDB_CONNECTION_STRING="${MDB_CONNECTION_STRING}" /bin/bash -eu -c "$(cat <<'EOF'
mongosh "${MDB_CONNECTION_STRING}" --quiet --eval '
  const db_mflix = db.getSiblingDB("sample_mflix");

  // Get a sample embedding to use as query vector
  const sampleMovie = db_mflix.embedded_movies.findOne(
    { plot_embedding_voyage_3_large: { $exists: true } },
    { title: 1, plot_embedding_voyage_3_large: 1 }
  );

  if (!sampleMovie || !sampleMovie.plot_embedding_voyage_3_large) {
    print("ERROR: No embedding found");
    quit(1);
  }

  print("Using embedding from: " + sampleMovie.title);
  print("");

  // Execute vector search
  const results = db_mflix.embedded_movies.aggregate([
    {
      $vectorSearch: {
        index: "vector_index",
        path: "plot_embedding_voyage_3_large",
        queryVector: sampleMovie.plot_embedding_voyage_3_large,
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
  ]).toArray();

  print("Found " + results.length + " similar movies:");
  results.forEach((r, i) => {
    print((i+1) + ". " + r.title + " (" + (r.year || "N/A") + ") - similarity: " + r.score.toFixed(4));
    if (r.plot) {
      print("   Plot: " + r.plot.substring(0, 80) + "...");
    }
  });
  print("COUNT:" + results.length);
'
EOF
)" 2>/dev/null)

echo "${result}"
count=$(echo "${result}" | grep "^COUNT:" | cut -d: -f2)
echo ""

echo "=== Vector Search Query Summary ==="
echo "Similar movies found: ${count:-0}"

if [[ "${count:-0}" -gt 0 ]]; then
  echo ""
  echo "SUCCESS: Vector search queries through mongos are working correctly"
else
  echo ""
  echo "ERROR: Vector search queries failed"
  exit 1
fi

