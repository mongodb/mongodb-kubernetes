kubectl --context "${K8S_CTX}" -n "${MDB_NS}" create configmap om-project \
        --from-literal=projectName="${MDB_RESOURCE_NAME}" --from-literal=baseUrl="${OPS_MANAGER_API_URL}" \
        --from-literal=orgId="${OPS_MANAGER_ORG_ID:-}"

