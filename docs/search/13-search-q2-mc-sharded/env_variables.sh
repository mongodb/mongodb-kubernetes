# ============================================================================
# Q2-MC Sharded — Multi-cluster MongoDBSearch with managed Envoy + external mongod
#
# Source this file with `source env_variables.sh` before running the snippets.
# Replace every `<...>` placeholder before sourcing.
# ============================================================================

# ============================================================================
# KUBERNETES CONTEXTS
# ============================================================================

# Central (operator) cluster context
export K8S_CENTRAL_CTX="<central cluster context>"

# Member clusters where mongot + Envoy run.
# Per spec §6.2 the load-test target is 2-3 member clusters.
export K8S_CLUSTER_0_CTX="<member cluster 0 context>"
export K8S_CLUSTER_1_CTX="<member cluster 1 context>"

# Member cluster names — these resolve {clusterName} in the externalHostname template.
export MEMBER_CLUSTER_0_NAME="us-east-k8s"
export MEMBER_CLUSTER_1_NAME="eu-west-k8s"

# Region tags (must match `region` tag on every external mongod member, per spec §6.4)
export MEMBER_CLUSTER_0_REGION="us-east"
export MEMBER_CLUSTER_1_REGION="eu-west"

# Namespace where MongoDBSearch will be deployed
export MDB_NS="mongodb"

# ============================================================================
# MONGODBSEARCH RESOURCE
# ============================================================================

export MDB_SEARCH_RESOURCE_NAME="lt-search-sharded"

# Mongot replicas PER SHARD per cluster (spec §4.2 — `clusters[].replicas` is
# pods per shard, not total).
export MDB_MONGOT_REPLICAS=2

# ============================================================================
# EXTERNAL SHARDED CLUSTER (customer-owned, off-cluster)
# ============================================================================
# Per spec §6.1 — mongos pool + per-shard 3-node replica sets, each shard's
# members tagged per region.

# Mongos router hosts
export MDB_EXTERNAL_MONGOS_HOST_0="<mongos-east-1.lt.example.com:27017>"
export MDB_EXTERNAL_MONGOS_HOST_1="<mongos-west-1.lt.example.com:27017>"

# Shard 0 members (per region — replSetConfig.members[].tags must carry `region`)
export MDB_SHARD_0_NAME="shard-0"
export MDB_SHARD_0_HOST_0="<shard0-east-1.lt.example.com:27018>"
export MDB_SHARD_0_HOST_1="<shard0-east-2.lt.example.com:27018>"
export MDB_SHARD_0_HOST_2="<shard0-west-1.lt.example.com:27018>"

# Shard 1 members
export MDB_SHARD_1_NAME="shard-1"
export MDB_SHARD_1_HOST_0="<shard1-east-1.lt.example.com:27018>"
export MDB_SHARD_1_HOST_1="<shard1-east-2.lt.example.com:27018>"
export MDB_SHARD_1_HOST_2="<shard1-west-1.lt.example.com:27018>"

# Sync-source user (pre-created on the external sharded cluster)
export MDB_SEARCH_SYNC_USERNAME="search-sync-source"

# ============================================================================
# CUSTOMER-REPLICATED SECRETS (per-cluster, same name in every cluster)
# ============================================================================
# Per spec §6.3.

# Sync-source password (key: password)
export MDB_SYNC_PASSWORD_SECRET="search-sync-password"

# CA bundle (key: ca.crt)
export MDB_EXTERNAL_CA_SECRET="external-ca"

# Sharded-only — keyfile shared with the external mongod cluster
export MDB_KEYFILE_SECRET="mongod-keyfile"

# TLS certs prefix
export MDB_TLS_CERT_SECRET_PREFIX="lt-prefix"

# ============================================================================
# LOAD BALANCER HOSTNAME TEMPLATE
# ============================================================================
# Per spec §4.2 — must contain BOTH {clusterName} (or {clusterIndex}) AND
# {shardName} for sharded multi-cluster.
export MDB_LB_EXTERNAL_HOSTNAME_TEMPLATE="{clusterName}.{shardName}.search-lb.lt.example.com:443"

# ============================================================================
# OPERATOR INSTALL
# ============================================================================

export OPERATOR_NAMESPACE="${MDB_NS}"
export OPERATOR_HELM_CHART="oci://quay.io/mongodb/helm-charts/mongodb-kubernetes"
export OPERATOR_ADDITIONAL_HELM_VALUES=""
