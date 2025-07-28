kubectl --context "${K8S_CLUSTER_0_CONTEXT_NAME}" --namespace "${MDB_NAMESPACE}" \
  create secret generic mdb-admin-user-password \
  --from-literal=password="${MDB_ADMIN_USER_PASSWORD}"

kubectl --context "${K8S_CLUSTER_0_CONTEXT_NAME}" --namespace "${MDB_NAMESPACE}" \
  create secret generic mdbc-rs-search-sync-source-password \
  --from-literal=password="${MDB_SEARCH_SYNC_USER_PASSWORD}"

kubectl --context "${K8S_CLUSTER_0_CONTEXT_NAME}" --namespace "${MDB_NAMESPACE}" \
  create secret generic mdb-user-password \
  --from-literal=password="${MDB_USER_PASSWORD}"

