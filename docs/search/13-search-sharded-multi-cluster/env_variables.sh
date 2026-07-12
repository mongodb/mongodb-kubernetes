# ============================================================================
# KUBERNETES CONFIGURATION
# ============================================================================

# Kubernetes context names for the member clusters (run: kubectl config get-contexts).
# Cluster 0 is also the central (operator) cluster. The clusters must be able to
# resolve and reach each other's Services (e.g. via a multi-primary Istio mesh).
export K8S_CTX_0="<central/member-0 cluster context>"
export K8S_CTX_1="<member-1 cluster context>"

# Namespace where the operator, MongoDB and MongoDB Search will be deployed.
# It is created in every member cluster.
export MDB_NS="mongodb"

# ============================================================================
# CLUSTER NAMING
# ============================================================================

# Name for the operator-managed multi-cluster sharded MongoDB and its
# MongoDBSearch resource. Unlike the single-cluster scenarios the names differ:
# a multi-cluster MongoDBSearch consumes the source as an external deployment
# (spec.source.external), so there is no same-name reference to infer.
export MDB_RESOURCE_NAME="mdb-mc-sh"
export MDB_SEARCH_RESOURCE_NAME="mdb-mc-sh-search"

# ============================================================================
# OPS MANAGER / CLOUD MANAGER
# ============================================================================

export OPS_MANAGER_PROJECT_NAME="<arbitrary project name>"
export OPS_MANAGER_API_URL="<SET API URL>"
export OPS_MANAGER_API_USER="<SET API USER>"
export OPS_MANAGER_API_KEY="<SET API KEY>"
export OPS_MANAGER_ORG_ID="<SET ORG ID>"

# ============================================================================
# MONGODB VERSION
# ============================================================================

# Minimum required MongoDB version for Search is 8.2
export MDB_VERSION="8.2.6-ent"

# ============================================================================
# USER CREDENTIALS (change these in production!)
# ============================================================================

export MDB_ADMIN_USER_PASSWORD="admin-user-password-CHANGE-ME"
export MDB_USER_PASSWORD="mdb-user-password-CHANGE-ME"
export MDB_SEARCH_SYNC_USER_PASSWORD="search-sync-user-password-CHANGE-ME"

# ============================================================================
# OPERATOR CONFIGURATION
# ============================================================================

# The multi-cluster operator install additionally requires the kubectl-mongodb
# plugin (kubectl mongodb multicluster setup) -- see 13_0100_install_operator.sh.
export OPERATOR_HELM_CHART="oci://quay.io/mongodb/helm-charts/mongodb-kubernetes"
export OPERATOR_ADDITIONAL_HELM_VALUES=""

# ============================================================================
# TLS CONFIGURATION
# ============================================================================

export MDB_TLS_CERT_SECRET_PREFIX="certs"
export MDB_TLS_CA_CONFIGMAP="${MDB_RESOURCE_NAME}-ca"

export CERT_MANAGER_NAMESPACE="cert-manager"
export MDB_TLS_SELF_SIGNED_ISSUER="selfsigned-bootstrap-issuer"
export MDB_TLS_CA_CERT_NAME="my-selfsigned-ca"
export MDB_TLS_CA_SECRET_NAME="root-secret"
export MDB_TLS_CA_ISSUER="my-ca-issuer"

# ============================================================================
# MULTI-CLUSTER SHARDED TOPOLOGY
# ============================================================================

# 3 shards; one mongod per shard, one mongos and one config-server member in
# each of the two member clusters.
export MDB_SHARD_COUNT=3
export MDB_MONGODS_PER_SHARD_PER_CLUSTER=1
export MDB_MONGOS_PER_CLUSTER=1
export MDB_CONFIG_SERVERS_PER_CLUSTER=1

# Shard names (derived from operator naming convention)
export MDB_SHARD_0_NAME="${MDB_RESOURCE_NAME}-0"
export MDB_SHARD_1_NAME="${MDB_RESOURCE_NAME}-1"
export MDB_SHARD_2_NAME="${MDB_RESOURCE_NAME}-2"

# ============================================================================
# SEARCH CONFIGURATION
# ============================================================================

# mongot replicas deployed per (cluster, shard)
export MDB_MONGOT_REPLICAS_PER_CLUSTER=1

# ============================================================================
# DERIVED VALUES (computed from topology + search config above)
# ============================================================================

# Envoy proxy port (operator default)
ENVOY_PROXY_PORT="27028"
export ENVOY_PROXY_PORT

# Cluster 0's Envoy proxy Services (operator-derived, do not change). Configure
# mongos with the cluster-level value and each shard's mongods with that shard's
# value for the Search server parameters.
export MDB_PROXY_HOST_0="${MDB_SEARCH_RESOURCE_NAME}-search-0-proxy-svc.${MDB_NS}.svc.cluster.local:${ENVOY_PROXY_PORT}"
export MDB_PROXY_HOST_SHARD_0="${MDB_SEARCH_RESOURCE_NAME}-search-0-${MDB_SHARD_0_NAME}-proxy-svc.${MDB_NS}.svc.cluster.local:${ENVOY_PROXY_PORT}"
export MDB_PROXY_HOST_SHARD_1="${MDB_SEARCH_RESOURCE_NAME}-search-0-${MDB_SHARD_1_NAME}-proxy-svc.${MDB_NS}.svc.cluster.local:${ENVOY_PROXY_PORT}"
export MDB_PROXY_HOST_SHARD_2="${MDB_SEARCH_RESOURCE_NAME}-search-0-${MDB_SHARD_2_NAME}-proxy-svc.${MDB_NS}.svc.cluster.local:${ENVOY_PROXY_PORT}"

# mongos router endpoints supplied by you. Replace each placeholder with the
# actual mongos host reachable from your Kubernetes cluster. For CI,
# env_variables_e2e_private.sh overrides these with the operator-managed
# Services (naming: <resource>-mongos-<clusterIndex>-<memberIndex>-svc).
export MDB_MONGOS_HOST_0="<your-mongos-cluster0-host:27017>"
export MDB_MONGOS_HOST_1="<your-mongos-cluster1-host:27017>"

# Per-shard mongod host:port for each member cluster supplied by you.
# Replace each placeholder. For CI, env_variables_e2e_private.sh overrides
# these (naming: <resource>-<shardIndex>-<clusterIndex>-<memberIndex>-svc).
export MDB_SHARD_0_HOST_CL0="<your-shard-0-cluster0-host:27017>"
export MDB_SHARD_0_HOST_CL1="<your-shard-0-cluster1-host:27017>"
export MDB_SHARD_1_HOST_CL0="<your-shard-1-cluster0-host:27017>"
export MDB_SHARD_1_HOST_CL1="<your-shard-1-cluster1-host:27017>"
export MDB_SHARD_2_HOST_CL0="<your-shard-2-cluster0-host:27017>"
export MDB_SHARD_2_HOST_CL1="<your-shard-2-cluster1-host:27017>"

# Connection strings (built from the mongos hosts above)
export MDB_ADMIN_CONNECTION_STRING="mongodb://mdb-admin:${MDB_ADMIN_USER_PASSWORD}@${MDB_MONGOS_HOST_0},${MDB_MONGOS_HOST_1}/?tls=true&tlsCAFile=/tls/ca.crt&authSource=admin&authMechanism=SCRAM-SHA-256"
export MDB_USER_CONNECTION_STRING="mongodb://mdb-user:${MDB_USER_PASSWORD}@${MDB_MONGOS_HOST_0},${MDB_MONGOS_HOST_1}/?tls=true&tlsCAFile=/tls/ca.crt&authSource=admin&authMechanism=SCRAM-SHA-256"
