echo "Executing vector search query..."
echo ""

user_conn="${MDB_USER_CONNECTION_STRING:-${MDB_CONNECTION_STRING}}"

# shellcheck disable=SC2016
kubectl exec mongodb-tools \
  -n "${MDB_NS}" \
  --context "${K8S_CTX}" \
  -- mongosh "${user_conn}" --quiet --eval '
  use("sample_mflix");

  const collections = db.getCollectionNames();
  if (!collections.includes("embedded_movies")) {
    console.log("Warning: embedded_movies collection not found.");
    console.log("Skipping vector search demo.");
    quit(0);
  }

  const sample = db.embedded_movies.findOne(
    { plot_embedding: { $exists: true } },
    { plot_embedding: 1, title: 1 }
  );

  if (!sample || !sample.plot_embedding) {
    console.log("Warning: No documents with plot_embedding found.");
    console.log("Skipping vector search demo.");
    quit(0);
  }

  console.log("Using embedding from: \"" + sample.title + "\"\n");

  const results = db.embedded_movies.aggregate([
    {
      $vectorSearch: {
        index: "vector_index",
        path: "plot_embedding",
        queryVector: sample.plot_embedding,
        numCandidates: 50,
        limit: 5
      }
    },
    {
      $project: {
        title: 1,
        year: 1,
        plot: 1,
        score: { $meta: "vectorSearchScore" }
      }
    }
  ]).toArray();

  if (results.length === 0) {
    console.log("ERROR: vector search query returned 0 results");
    quit(1);
  }

  console.log("Top 5 similar movies:\n");

  results.forEach((doc, i) => {
    console.log((i + 1) + ". \"" + doc.title + "\" (" + (doc.year || "N/A") + ")");
    console.log("   Similarity: " + (doc.score * 100).toFixed(2) + "%");
    if (doc.plot) {
      console.log("   Plot: " + doc.plot.substring(0, 80) + "...");
    }
    console.log("");
  });
'

echo ""
echo "Vector search query executed successfully"
