kubectl --context "${K8S_CLUSTER_0_CONTEXT_NAME}" --namespace "${MDB_NAMESPACE}" \
  create secret generic admin-user-password \
  --from-literal=password="${MDB_ADMIN_USER_PASSWORD}"

kubectl --context "${K8S_CLUSTER_0_CONTEXT_NAME}" --namespace "${MDB_NAMESPACE}" \
  create secret generic search-user-password \
  --from-literal=password="${MDB_SEARCH_USER_PASSWORD}"
