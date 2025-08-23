kubectl --context "${K8S_CTX}" -n "${MDB_NS}" create configmap om-project \
        --from-literal=projectName="${MDB_NS}" --from-literal=baseUrl="${BASE_URL}" \
        --from-literal=orgId="${OM_ORGID:-}"

