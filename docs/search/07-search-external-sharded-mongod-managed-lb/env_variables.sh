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

export MDB_TLS_CERT_SECRET_PREFIX="certs"
export MDB_TLS_CA_CONFIGMAP="${MDB_EXTERNAL_CLUSTER_NAME}-ca"

export CERT_MANAGER_NAMESPACE="cert-manager"
export MDB_TLS_SELF_SIGNED_ISSUER="selfsigned-bootstrap-issuer"
export MDB_TLS_CA_CERT_NAME="my-selfsigned-ca"
export MDB_TLS_CA_SECRET_NAME="root-secret"
export MDB_TLS_CA_ISSUER="my-ca-issuer"

# ============================================================================
# EXTERNAL CLUSTER TOPOLOGY (fill in your actual values)
# ============================================================================
# Your external MongoDB sharded cluster information.
# If running on VMs or bare metal, replace with your actual hostnames.
# The defaults below match the simulated K8s cluster for testing.

# -- Shard 0 --
export MDB_EXTERNAL_SHARD_0_NAME="ext-mdb-sh-0"
export MDB_EXTERNAL_SHARD_0_HOST="ext-mdb-sh-0-0.ext-mdb-sh-sh.${MDB_NS}.svc.cluster.local:27017"

# -- Shard 1 --
export MDB_EXTERNAL_SHARD_1_NAME="ext-mdb-sh-1"
export MDB_EXTERNAL_SHARD_1_HOST="ext-mdb-sh-1-0.ext-mdb-sh-sh.${MDB_NS}.svc.cluster.local:27017"

# -- Mongos router --
export MDB_EXTERNAL_MONGOS_HOST="ext-mdb-sh-mongos-0.ext-mdb-sh-svc.${MDB_NS}.svc.cluster.local:27017"

# -- Pod/container names (for verification scripts only) --
export MDB_EXTERNAL_SHARD_0_POD="ext-mdb-sh-0-0"
export MDB_EXTERNAL_SHARD_1_POD="ext-mdb-sh-1-0"
export MDB_EXTERNAL_MONGOS_POD="ext-mdb-sh-mongos-0"

# ============================================================================
# SEARCH CONFIGURATION
# ============================================================================
export MDB_MONGOT_REPLICAS=2

# ============================================================================
# DERIVED VALUES (computed from topology + search config above)
# ============================================================================
# Proxy service names — the operator creates these as:
#   {search-resource}-search-0-{shard-name}-proxy-svc
export MDB_PROXY_SVC_SHARD_0="${MDB_SEARCH_RESOURCE_NAME}-search-0-${MDB_EXTERNAL_SHARD_0_NAME}-proxy-svc"
export MDB_PROXY_SVC_SHARD_1="${MDB_SEARCH_RESOURCE_NAME}-search-0-${MDB_EXTERNAL_SHARD_1_NAME}-proxy-svc"
export MDB_PROXY_HOST_SHARD_0="${MDB_PROXY_SVC_SHARD_0}.${MDB_NS}.svc.cluster.local:27029"
export MDB_PROXY_HOST_SHARD_1="${MDB_PROXY_SVC_SHARD_1}.${MDB_NS}.svc.cluster.local:27029"

# Connection strings (built from mongos host)
export MDB_ADMIN_CONNECTION_STRING="mongodb://mdb-admin:${MDB_ADMIN_USER_PASSWORD}@${MDB_EXTERNAL_MONGOS_HOST}/?tls=true&tlsCAFile=/tls/ca-pem&authSource=admin&authMechanism=SCRAM-SHA-256"
export MDB_USER_CONNECTION_STRING="mongodb://mdb-user:${MDB_USER_PASSWORD}@${MDB_EXTERNAL_MONGOS_HOST}/?tls=true&tlsCAFile=/tls/ca-pem&authSource=admin&authMechanism=SCRAM-SHA-256"

# Legacy connection string (TLS with ca.crt path)
export MDB_CONNECTION_STRING="mongodb://mdb-user:${MDB_USER_PASSWORD}@${MDB_EXTERNAL_MONGOS_HOST}/?tls=true&tlsCAFile=/tls/ca.crt"
