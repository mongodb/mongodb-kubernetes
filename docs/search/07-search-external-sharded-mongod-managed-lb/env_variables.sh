# Environment Variables for MongoDB Search with External Sharded MongoDB + Managed Envoy LB
#
# This configuration deploys MongoDB Search against an EXTERNAL sharded MongoDB cluster
# (mongod runs outside the operator's management) with MANAGED Envoy load balancing.
#
# What "Managed Envoy" means:
#   - The operator automatically deploys and configures an Envoy L7 proxy
#   - You do NOT need to create Envoy ConfigMaps, Deployments, or Services manually
#   - The operator creates per-shard proxy Services for SNI-based routing
#
# Traffic flow: External mongod → Envoy (operator-managed) → mongot
#
# For testing purposes, we simulate an external cluster by deploying a MongoDB Enterprise
# sharded cluster in the same Kubernetes cluster, then configuring MongoDBSearch to treat
# it as an external source using spec.source.external.shardedCluster.

# ============================================================================
# KUBERNETES CONFIGURATION
# ============================================================================

# Your Kubernetes context name (run: kubectl config get-contexts)
export K8S_CTX="<local cluster context>"

# Namespace where MongoDB Search and simulated external cluster will be deployed
export MDB_NS="mongodb"

# ============================================================================
# CLUSTER NAMING
# ============================================================================

# Name for the simulated external MongoDB sharded cluster
# In production, this would be your actual external cluster identifier
export MDB_EXTERNAL_CLUSTER_NAME="ext-mdb-sh"

# MongoDB Search resource name (different from MDB name since it's "external")
export MDB_SEARCH_RESOURCE_NAME="ext-search"

# ============================================================================
# SHARDED CLUSTER CONFIGURATION
# ============================================================================

# Number of shards in the external cluster
export MDB_SHARD_COUNT=2

# Members per shard (for simulated cluster)
export MDB_MONGODS_PER_SHARD=1

# Number of mongos routers
export MDB_MONGOS_COUNT=1

# Config server replica set members
export MDB_CONFIG_SERVER_COUNT=2

# Number of mongot replicas per shard (for high availability)
# The operator deploys this many mongot pods per shard, all fronted by Envoy
export MDB_MONGOT_REPLICAS=2

# ============================================================================
# OPS MANAGER / CLOUD MANAGER (for simulated external cluster)
# ============================================================================

export OPS_MANAGER_PROJECT_NAME="<arbitrary project name>"
export OPS_MANAGER_API_URL="https://cloud-qa.mongodb.com"
export OPS_MANAGER_API_USER="<SET API USER>"
export OPS_MANAGER_API_KEY="<SET API KEY>"
export OPS_MANAGER_ORG_ID="<SET ORG ID>"

# ============================================================================
# MONGODB VERSION
# ============================================================================

# Minimum required MongoDB version for Search is 8.2.0
export MDB_VERSION="8.2.0-ent"

# ============================================================================
# USER CREDENTIALS (change these in production!)
# ============================================================================

export MDB_ADMIN_USER_PASSWORD="admin-user-password-CHANGE-ME"
export MDB_USER_PASSWORD="mdb-user-password-CHANGE-ME"
export MDB_SEARCH_SYNC_USER_PASSWORD="search-sync-user-password-CHANGE-ME"

# ============================================================================
# OPERATOR CONFIGURATION
# ============================================================================

export OPERATOR_HELM_CHART="mongodb/mongodb-kubernetes"
export OPERATOR_ADDITIONAL_HELM_VALUES=""

# ============================================================================
# TLS CONFIGURATION
# ============================================================================

export MDB_TLS_ENABLED="true"
export MDB_TLS_CERT_SECRET_PREFIX="certs"
export MDB_TLS_CA_CONFIGMAP="${MDB_EXTERNAL_CLUSTER_NAME}-ca"

export CERT_MANAGER_NAMESPACE="cert-manager"
export MDB_TLS_SELF_SIGNED_ISSUER="selfsigned-bootstrap-issuer"
export MDB_TLS_CA_CERT_NAME="my-selfsigned-ca"
export MDB_TLS_CA_SECRET_NAME="root-secret"
export MDB_TLS_CA_ISSUER="my-ca-issuer"

# ============================================================================
# EXTERNAL CLUSTER RESOURCE NAMES
# ============================================================================
# These names identify the components of the external MongoDB cluster.
# Default values match the Kubernetes operator naming convention for the
# simulated cluster. Override them when pointing at a real external cluster.

# Shard names (space-separated, must match MDB_SHARD_COUNT)
export MDB_EXTERNAL_SHARD_NAMES="ext-mdb-sh-0 ext-mdb-sh-1"

# Headless service (or hostname suffix) for shard members
export MDB_EXTERNAL_SHARD_SVC="ext-mdb-sh-sh"

# Config server resource name and headless service
export MDB_EXTERNAL_CONFIG_RS_NAME="ext-mdb-sh-config"
export MDB_EXTERNAL_CONFIG_SVC="ext-mdb-sh-cs"

# Mongos resource name and service
export MDB_EXTERNAL_MONGOS_NAME="ext-mdb-sh-mongos"
export MDB_EXTERNAL_MONGOS_SVC="ext-mdb-sh-svc"

# ============================================================================
# ENVOY PROXY CONFIGURATION (Managed by Operator)
# ============================================================================

# Port where Envoy listens for mongod connections (operator default)
export ENVOY_PROXY_PORT="27029"

# NOTE: Unlike unmanaged mode, you do NOT need to specify:
# - ENVOY_IMAGE (operator uses its default)
# - Envoy ConfigMap or Deployment YAML
# - Per-shard proxy Services
# The operator handles all of this automatically!

# ============================================================================
# CONNECTION STRING
# ============================================================================

# Connection string for mongos (sharded cluster entry point) with TLS
export MDB_CONNECTION_STRING="mongodb://mdb-user:${MDB_USER_PASSWORD}@${MDB_EXTERNAL_MONGOS_NAME}-0.${MDB_EXTERNAL_MONGOS_SVC}.${MDB_NS}.svc.cluster.local:27017/?tls=true&tlsCAFile=/tls/ca.crt"
