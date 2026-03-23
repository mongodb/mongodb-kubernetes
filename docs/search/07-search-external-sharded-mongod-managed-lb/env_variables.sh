

# ======================================================================
# KUBERNETES CONFIGURATION
# ======================================================================

# Your Kubernetes context name
# (run: kubectl config get-contexts)
export K8S_CTX="<local cluster context>"

# Namespace for MongoDB Search and the
# external cluster
export MDB_NS="mongodb"

# ======================================================================
# CLUSTER NAMING
# ======================================================================

# Name for the external MongoDB sharded cluster
export MDB_EXTERNAL_CLUSTER_NAME="ext-mdb-sh"

# MongoDB Search resource name
# (different from MDB name since it's "external")
export MDB_SEARCH_RESOURCE_NAME="ext-search"

# ======================================================================
# OPS MANAGER / CLOUD MANAGER
# ======================================================================

export OPS_MANAGER_PROJECT_NAME="<arbitrary project name>"
export OPS_MANAGER_API_URL="<SET API URL>"
export OPS_MANAGER_API_USER="<SET API USER>"
export OPS_MANAGER_API_KEY="<SET API KEY>"
export OPS_MANAGER_ORG_ID="<SET ORG ID>"

# ======================================================================
# MONGODB VERSION
# ======================================================================

# Minimum required MongoDB version for Search is 8.2.0
export MDB_VERSION="8.2.0-ent"

# ======================================================================
# USER CREDENTIALS (change these in production!)
# ======================================================================

export MDB_ADMIN_USER_PASSWORD="admin-user-password-CHANGE-ME"
export MDB_USER_PASSWORD="mdb-user-password-CHANGE-ME"
export MDB_SEARCH_SYNC_USER_PASSWORD="search-sync-user-password-CHANGE-ME"

# ======================================================================
# OPERATOR CONFIGURATION
# ======================================================================

HELM_REPO="oci://quay.io/mongodb/helm-charts"
export OPERATOR_HELM_CHART="${HELM_REPO}/mongodb-kubernetes"
export OPERATOR_ADDITIONAL_HELM_VALUES=""

# ======================================================================
# TLS CONFIGURATION
# ======================================================================

export MDB_TLS_CERT_SECRET_PREFIX="certs"
export MDB_TLS_CA_CONFIGMAP="${MDB_EXTERNAL_CLUSTER_NAME}-ca"

export CERT_MANAGER_NAMESPACE="cert-manager"
export MDB_TLS_SELF_SIGNED_ISSUER="selfsigned-bootstrap-issuer"
export MDB_TLS_CA_CERT_NAME="my-selfsigned-ca"
export MDB_TLS_CA_SECRET_NAME="root-secret"
export MDB_TLS_CA_ISSUER="my-ca-issuer"

# ======================================================================
# EXTERNAL CLUSTER TOPOLOGY (fill in your actual values)
# ======================================================================
# Your external MongoDB sharded cluster information.
# Replace with your actual hostnames.

# External domain used with
# spec.externalAccess.externalDomain on the MongoDB
# CR. When set, mongos pods are reachable at
# {podName}.{externalDomain}.
export MDB_EXTERNAL_DOMAIN="ext-mdb.example.com"

# -- Shard 0 --
export MDB_EXTERNAL_SHARD_0_NAME="ext-mdb-sh-0"
MDB_SH_SVC="${MDB_NS}.svc.cluster.local:27017"
export MDB_EXTERNAL_SHARD_0_HOST=\
"ext-mdb-sh-0-0.ext-mdb-sh-sh.${MDB_SH_SVC}"

# -- Shard 1 --
export MDB_EXTERNAL_SHARD_1_NAME="ext-mdb-sh-1"
export MDB_EXTERNAL_SHARD_1_HOST=\
"ext-mdb-sh-1-0.ext-mdb-sh-sh.${MDB_SH_SVC}"

# -- Mongos router (uses external domain) --
MDB_MONGOS_PREFIX="${MDB_EXTERNAL_CLUSTER_NAME}-mongos-0"
export MDB_EXTERNAL_MONGOS_HOST=\
"${MDB_MONGOS_PREFIX}.${MDB_EXTERNAL_DOMAIN}:27017"

# ======================================================================
# SEARCH CONFIGURATION
# ======================================================================
export MDB_MONGOT_REPLICAS=2

# ======================================================================
# DERIVED VALUES (computed from topology + search config)
# ======================================================================
# DO NOT CHANGE these proxy service names.
# The operator derives them via
# LoadBalancerProxyServiceNameForShard
# (api/v1/search/mongodbsearch_types.go) using the
# hardcoded pattern:
#   {search-resource}-search-0-{shard-name}-proxy-svc
# Changing these vars won't change the real Services
# -- it will just break the scripts.
SEARCH_PFX="${MDB_SEARCH_RESOURCE_NAME}-search-0"
export MDB_PROXY_SVC_SHARD_0=\
"${SEARCH_PFX}-${MDB_EXTERNAL_SHARD_0_NAME}-proxy-svc"
export MDB_PROXY_SVC_SHARD_1=\
"${SEARCH_PFX}-${MDB_EXTERNAL_SHARD_1_NAME}-proxy-svc"

SVC_SUFFIX="${MDB_NS}.svc.cluster.local:27029"
export MDB_PROXY_HOST_SHARD_0=\
"${MDB_PROXY_SVC_SHARD_0}.${SVC_SUFFIX}"
export MDB_PROXY_HOST_SHARD_1=\
"${MDB_PROXY_SVC_SHARD_1}.${SVC_SUFFIX}"

# Connection strings (built from mongos host)
MDB_TLS_OPTS="tls=true&tlsCAFile=/tls/ca-pem"
MDB_AUTH_OPTS="authSource=admin&authMechanism=SCRAM-SHA-256"
MDB_CONN_OPTS="?${MDB_TLS_OPTS}&${MDB_AUTH_OPTS}"

export MDB_ADMIN_CONNECTION_STRING=\
"mongodb://mdb-admin:${MDB_ADMIN_USER_PASSWORD}@${MDB_EXTERNAL_MONGOS_HOST}/${MDB_CONN_OPTS}"
export MDB_USER_CONNECTION_STRING=\
"mongodb://mdb-user:${MDB_USER_PASSWORD}@${MDB_EXTERNAL_MONGOS_HOST}/${MDB_CONN_OPTS}"
