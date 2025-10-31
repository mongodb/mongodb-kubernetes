create_secret() {
    local secret_name="$1"
    local password_var="$2"

    kubectl create secret generic "${secret_name}" \
        --from-literal=password="${password_var}" \
        --dry-run=client -o yaml \
        | kubectl apply --context "${K8S_CTX}" --namespace "${MDB_NS}" -f -
}

create_secret "mdb-admin-user-password" "${MDB_ADMIN_USER_PASSWORD}"
create_secret "${MDB_RESOURCE_NAME}-search-sync-source-password" "${MDB_SEARCH_SYNC_USER_PASSWORD}"
create_secret "mdb-user-password" "${MDB_USER_PASSWORD}"
