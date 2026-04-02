# E2E Test Environment - Public Configuration
#
# Sources the default environment and overrides
# CI-specific values for public testing.

source "$(dirname "${BASH_SOURCE[0]}")/env_variables.sh"

export K8S_CTX="kind-kind"

# Simulated external cluster
export MDB_EXTERNAL_CLUSTER_NAME="ext-mdb-sh"
export MDB_EXTERNAL_DOMAIN="ext-mdb.example.com"
export MDB_TLS_CA_CONFIGMAP="${MDB_EXTERNAL_CLUSTER_NAME}-ca"
export MDB_EXTERNAL_SHARD_0_NAME="ext-mdb-sh-0"
export MDB_EXTERNAL_SHARD_0_HOST="${MDB_EXTERNAL_CLUSTER_NAME}-0-0.${MDB_EXTERNAL_DOMAIN}:27017"
export MDB_EXTERNAL_SHARD_1_NAME="ext-mdb-sh-1"
export MDB_EXTERNAL_SHARD_1_HOST="${MDB_EXTERNAL_CLUSTER_NAME}-1-0.${MDB_EXTERNAL_DOMAIN}:27017"
export MDB_EXTERNAL_MONGOS_HOST="${MDB_EXTERNAL_CLUSTER_NAME}-mongos-0.${MDB_EXTERNAL_DOMAIN}:27017"

# Derived values
SEARCH_PFX="${MDB_SEARCH_RESOURCE_NAME}-search-0"
export MDB_PROXY_SVC_SHARD_0="${SEARCH_PFX}-${MDB_EXTERNAL_SHARD_0_NAME}-proxy-svc"
export MDB_PROXY_SVC_SHARD_1="${SEARCH_PFX}-${MDB_EXTERNAL_SHARD_1_NAME}-proxy-svc"
SVC_SUFFIX="${MDB_NS}.svc.cluster.local:27028"
export MDB_PROXY_HOST_SHARD_0="${MDB_PROXY_SVC_SHARD_0}.${SVC_SUFFIX}"
export MDB_PROXY_HOST_SHARD_1="${MDB_PROXY_SVC_SHARD_1}.${SVC_SUFFIX}"

# Connection strings
MDB_TLS_OPTS="tls=true&tlsCAFile=/tls/ca-pem"
MDB_AUTH_OPTS="authSource=admin&authMechanism=SCRAM-SHA-256"
MDB_CONN_OPTS="?${MDB_TLS_OPTS}&${MDB_AUTH_OPTS}"
export MDB_ADMIN_CONNECTION_STRING="mongodb://mdb-admin:${MDB_ADMIN_USER_PASSWORD}@${MDB_EXTERNAL_MONGOS_HOST}/${MDB_CONN_OPTS}"
export MDB_USER_CONNECTION_STRING="mongodb://mdb-user:${MDB_USER_PASSWORD}@${MDB_EXTERNAL_MONGOS_HOST}/${MDB_CONN_OPTS}"

# Ops Manager
export OPS_MANAGER_PROJECT_NAME="${NAMESPACE}-${MDB_EXTERNAL_CLUSTER_NAME}"
export OPS_MANAGER_API_URL="${OM_BASE_URL}"
export OPS_MANAGER_API_USER="${OM_USER}"
export OPS_MANAGER_API_KEY="${OM_API_KEY}"
export OPS_MANAGER_ORG_ID="${OM_ORGID}"
