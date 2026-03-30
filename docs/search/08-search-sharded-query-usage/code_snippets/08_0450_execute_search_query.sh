echo "Executing text search query" \
  "for 'drama adventure'..."
echo ""

user_conn="${MDB_USER_CONNECTION_STRING:-${MDB_CONNECTION_STRING}}"

mdb_script=$(cat <<'MONGOSH'
use sample_mflix;
db.movies.aggregate([
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
      _id: 0,
      title: 1,
      year: 1,
      plot: 1,
      score: { $meta: "searchScore" }
    }
  },
  { $limit: 5 }
]);
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
