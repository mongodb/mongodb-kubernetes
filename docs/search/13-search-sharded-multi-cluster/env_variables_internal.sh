# Internal source-deployment settings used only by automated tests.
export MDB_RESOURCE_NAME="${MDB_EXTERNAL_CLUSTER_NAME}"
export MDB_SHARD_COUNT=3
export MDB_MONGODS_PER_SHARD_PER_CLUSTER=1
export MDB_MONGOS_PER_CLUSTER=1
export MDB_CONFIG_SERVERS_PER_CLUSTER=1

export MDB_SHARD_0_NAME="${MDB_EXTERNAL_SHARD_0_NAME}"
export MDB_SHARD_1_NAME="${MDB_EXTERNAL_SHARD_1_NAME}"
export MDB_SHARD_2_NAME="${MDB_EXTERNAL_SHARD_2_NAME}"

export OPS_MANAGER_PROJECT_NAME="<arbitrary project name>"
export OPS_MANAGER_API_URL="<SET API URL>"
export OPS_MANAGER_API_USER="<SET API USER>"
export OPS_MANAGER_API_KEY="<SET API KEY>"
export OPS_MANAGER_ORG_ID="<SET ORG ID>"

export MDB_ADMIN_USER_PASSWORD="admin-user-password-CHANGE-ME"
export MDB_USER_PASSWORD="mdb-user-password-CHANGE-ME"
export MDB_SEARCH_SYNC_USER_PASSWORD="search-sync-user-password-CHANGE-ME"

export MDB_TLS_SELF_SIGNED_ISSUER="selfsigned-bootstrap-issuer"
export MDB_TLS_CA_CERT_NAME="my-selfsigned-ca"
export MDB_TLS_CA_SECRET_NAME="root-secret"
