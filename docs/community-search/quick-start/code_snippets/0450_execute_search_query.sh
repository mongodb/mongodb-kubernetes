#!/bin/bash

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

kubectl exec --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" mongodb-tools-pod -- /bin/bash -eu -c "$(cat <<EOF
echo '${mdb_script}' > /tmp/mdb_script.js
mongosh --quiet "mongodb://mdb-user:${MDB_USER_PASSWORD}@mdbc-rs-0.mdbc-rs-svc.${MDB_NAMESPACE}.svc.cluster.local:27017/?replicaSet=mdbc-rs" < /tmp/mdb_script.js
EOF
)"
