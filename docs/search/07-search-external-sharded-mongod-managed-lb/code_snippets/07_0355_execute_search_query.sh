#!/usr/bin/env bash
# Execute a text search query through mongos

echo "Executing text search query for 'drama adventure'..."
echo ""

user_conn="mongodb://mdb-user:${MDB_USER_PASSWORD}@${MDB_EXTERNAL_CLUSTER_NAME}-mongos-0.${MDB_EXTERNAL_CLUSTER_NAME}-svc.${MDB_NS}.svc.cluster.local:27017/?tls=true&tlsCAFile=/tls/ca-pem&authSource=admin&authMechanism=SCRAM-SHA-256"

# shellcheck disable=SC2016
kubectl exec mongodb-tools -n "${MDB_NS}" --context "${K8S_CTX}" -- mongosh "${user_conn}" --quiet --eval '
  use sample_mflix;

  print("Running $search aggregation pipeline...\n");

  const results = db.movies.aggregate([
    {
      $search: {
        index: "default",
        text: {
          query: "drama adventure",
          path: { wildcard: "*" }
        }
      }
    },
    {
      $project: {
        title: 1,
        year: 1,
        plot: 1,
        score: { $meta: "searchScore" }
      }
    },
    { $limit: 5 }
  ]).toArray();

  print("Top 5 search results:\n");

  results.forEach((doc, i) => {
    print(`${i + 1}. "${doc.title}" (${doc.year || "N/A"})`);
    print(`   Score: ${doc.score.toFixed(4)}`);
    if (doc.plot) {
      print(`   Plot: ${doc.plot.substring(0, 100)}...`);
    }
    print("");
  });

  print(`Total results shown: ${results.length}`);
'

echo ""
echo "✓ Search query executed successfully"
