mdb_script=$(cat <<'EOF'
use sample_mflix;
db.movies.aggregate([
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
]);
EOF
)

kubectl exec --context "${K8S_CTX}" -n "${MDB_NS}" \
  mongodb-tools-pod -- /bin/bash -eu -c "$(cat <<EOF
echo '${mdb_script}' > /tmp/mdb_script.js
<<<<<<<< HEAD:docs/community-search/quick-start/code_snippets/0450_execute_search_query.sh
mongosh --quiet "mongodb://search-user:${MDB_SEARCH_USER_PASSWORD}@mdbc-rs-0.mdbc-rs-svc.${MDB_NAMESPACE}.svc.cluster.local:27017/?replicaSet=mdbc-rs" < /tmp/mdb_script.js
========
mongosh --quiet "${MDB_CONNECTION_STRING}" < /tmp/mdb_script.js
>>>>>>>> 6309cff6f (Refactor of existing snippets on master):docs/search/03-search-query-usage/code_snippets/03_0450_execute_search_query.sh
EOF
)"
