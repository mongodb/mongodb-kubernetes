echo "Executing text search query" \
  "for 'drama adventure'..."
echo ""

user_conn="${MDB_USER_CONNECTION_STRING:-${MDB_CONNECTION_STRING}}"

# shellcheck disable=SC2016
kubectl exec mongodb-tools \
  -n "${MDB_NS}" \
  --context "${K8S_CTX}" \
  -- mongosh "${user_conn}" --quiet --eval '
  use("sample_mflix");

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

  if (results.length === 0) {
    console.log("ERROR: search query returned 0 results");
    quit(1);
  }

  console.log("Top 5 search results:\n");

  results.forEach((doc, i) => {
    console.log((i + 1) + ". \"" + doc.title + "\" (" + (doc.year || "N/A") + ")");
    console.log("   Score: " + doc.score.toFixed(4));
    if (doc.plot) {
      console.log("   Plot: " + doc.plot.substring(0, 100) + "...");
    }
    console.log("");
  });

  console.log("Total results shown: " + results.length);
'

echo ""
echo "Search query executed successfully"
