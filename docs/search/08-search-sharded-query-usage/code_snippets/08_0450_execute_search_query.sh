echo "Executing text search query..."
echo ""

user_conn="${MDB_USER_CONNECTION_STRING:-${MDB_CONNECTION_STRING}}"

mdb_script=$(cat <<'MONGOSH'
use sample_mflix;
const results = db.movies.aggregate([
  {
    $search: {
      "compound": {
        "must": [ {
          "text": {
            "query": "baseball",
            "path": "plot"
          }
        }],
        "mustNot": [ {
          "text": {
            "query": ["Comedy", "Romance"],
            "path": "genres"
          }
        } ]
      },
      "sort": {
        "released": -1
      }
    }
  },
  {
    $limit: 3
  },
  {
    $project: {
      "_id": 0,
      "title": 1,
      "plot": 1,
      "genres": 1,
      "released": 1
    }
  }
]).toArray();
printjson(results);
print("Result count: " + results.length);
if (results.length === 0) {
  print("ASSERTION FAILED: search query returned no documents");
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
echo "Search query executed successfully"
