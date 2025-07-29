#!/bin/bash

<<<<<<< Updated upstream
# Currently it's not possible to check the status of search indexes, we need to just wait
echo "Sleeping to wait for search indexes to be created"
sleep 60
=======
for _ in $(seq 0 10); do
  search_index_status=$(kubectl exec --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" mongodb-tools-pod -- \
      mongosh --quiet "mongodb://mdb-user:${MDB_USER_PASSWORD}@mdbc-rs-0.mdbc-rs-svc.${MDB_NAMESPACE}.svc.cluster.local:27017/?replicaSet=mdbc-rs" \
        --eval "use sample_mflix" \
        --eval 'db.movies.getSearchIndexes("default")[0]["status"]')

  if [[ "${search_index_status}" == "READY" ]]; then
    echo "Search index is ready."
    break
  fi
  echo "Search index is not ready yet: status=${search_index_status}"
  sleep 2
done

if [[ "${search_index_status}" != "READY" ]]; then
  echo "Error waiting for the search index to be ready"
  return 1
fi
>>>>>>> Stashed changes
