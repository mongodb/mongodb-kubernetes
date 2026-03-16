#!/usr/bin/env bash
# Execute a vector search query (if embedded_movies collection exists)
#
# Vector search finds documents similar to a given vector embedding.
# This uses a sample embedding to find similar movies.

echo "Executing vector search query..."
echo ""

user_conn="mongodb://mdb-user:${MDB_USER_PASSWORD}@${MDB_EXTERNAL_CLUSTER_NAME}-mongos-0.${MDB_EXTERNAL_CLUSTER_NAME}-svc.${MDB_NS}.svc.cluster.local:27017/?tls=true&tlsCAFile=/tls/ca-pem&authSource=admin&authMechanism=SCRAM-SHA-256"

# shellcheck disable=SC2016
kubectl exec mongodb-tools -n "${MDB_NS}" --context "${K8S_CTX}" -- mongosh "${user_conn}" --quiet --eval '
  use sample_mflix;

  // Check if embedded_movies collection exists
  const collections = db.getCollectionNames();
  if (!collections.includes("embedded_movies")) {
    print("Warning: embedded_movies collection not found.");
    print("Skipping vector search demo.");
    quit(0);
  }

  // Get a sample embedding from the first document
  const sample = db.embedded_movies.findOne(
    { plot_embedding: { $exists: true } },
    { plot_embedding: 1, title: 1 }
  );

  if (!sample || !sample.plot_embedding) {
    print("Warning: No documents with plot_embedding found.");
    print("Skipping vector search demo.");
    quit(0);
  }

  print(`Using embedding from: "${sample.title}"\n`);
  print("Finding similar movies...\n");

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

  print("Top 5 similar movies:\n");

  results.forEach((doc, i) => {
    print(`${i + 1}. "${doc.title}" (${doc.year || "N/A"})`);
    print(`   Similarity: ${(doc.score * 100).toFixed(2)}%`);
    if (doc.plot) {
      print(`   Plot: ${doc.plot.substring(0, 80)}...`);
    }
    print("");
  });
'

echo ""
echo "✓ Vector search query executed successfully"
