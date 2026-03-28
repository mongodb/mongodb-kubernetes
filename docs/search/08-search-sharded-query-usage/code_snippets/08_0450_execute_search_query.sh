echo "Executing text search query" \
  "for 'drama adventure'..."
echo ""

user_conn="${MDB_USER_CONNECTION_STRING:-${MDB_CONNECTION_STRING}}"

# shellcheck disable=SC2016
kubectl exec mongodb-tools \
  -n "${MDB_NS}" \
  --context "${K8S_CTX}" \
  -- mongosh "${user_conn}" --quiet --eval '
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
echo "Search query executed successfully"
