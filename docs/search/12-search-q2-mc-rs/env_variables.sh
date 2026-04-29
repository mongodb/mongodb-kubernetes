# ============================================================================
# Q2-MC ReplicaSet — Multi-cluster MongoDBSearch with managed Envoy + external mongod
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

# Member cluster names — these are the values that resolve {clusterName}
# in `loadBalancer.managed.externalHostname`. They MUST match the names used
# in `clusters[].clusterName` in the MongoDBSearch spec.
export MEMBER_CLUSTER_0_NAME="us-east-k8s"
export MEMBER_CLUSTER_1_NAME="eu-west-k8s"

# Region tags on each member cluster's external mongod (replSetConfig.members[].tags)
# Per spec §6.4: every external mongod member must carry a `region` tag matching
# `clusters[].syncSourceSelector.matchTags`.
export MEMBER_CLUSTER_0_REGION="us-east"
export MEMBER_CLUSTER_1_REGION="eu-west"

# Namespace where MongoDBSearch will be deployed (same in every member cluster)
export MDB_NS="mongodb"

# ============================================================================
# MONGODBSEARCH RESOURCE
# ============================================================================

export MDB_SEARCH_RESOURCE_NAME="lt-search"

# Mongot replicas per member cluster (>= 1 per spec §4.1)
export MDB_MONGOT_REPLICAS=2

# ============================================================================
# EXTERNAL MONGODB REPLICA SET (customer-owned, off-cluster)
# ============================================================================
# Per spec §6.1 — a 3-node external replica set with members across 2-3 regions,
# each member tagged in replSetConfig with `region: <region>`.

export MDB_EXTERNAL_HOST_0="<mongod-east-1.lt.example.com:27017>"
export MDB_EXTERNAL_HOST_1="<mongod-east-2.lt.example.com:27017>"
export MDB_EXTERNAL_HOST_2="<mongod-west-1.lt.example.com:27017>"

# Sync-source user — pre-created on the external mongod with `searchCoordinator`
# (or equivalent) role. Password lives in the secret named below.
export MDB_SEARCH_SYNC_USERNAME="search-sync-source"

# ============================================================================
# CUSTOMER-REPLICATED SECRETS (per-cluster, same name in every cluster)
# ============================================================================
# Per spec §6.3 — the load tester replicates these into every member cluster.

# Sync-source password (key: password)
export MDB_SYNC_PASSWORD_SECRET="search-sync-password"

# CA bundle (key: ca.crt)
export MDB_EXTERNAL_CA_SECRET="external-ca"

# TLS certs prefix — operator looks up `<prefix>-<unit-name>-cert` per cluster
export MDB_TLS_CERT_SECRET_PREFIX="lt-prefix"

# ============================================================================
# LOAD BALANCER HOSTNAME TEMPLATE
# ============================================================================
# Per spec §4.1 — must contain {clusterName} (or {clusterIndex}) so each member
# cluster gets its own resolvable hostname. The load tester is responsible for
# DNS pointing each {clusterName}.search-lb.lt.example.com at the local cloud
# LB / ingress fronting Envoy in that cluster.
export MDB_LB_EXTERNAL_HOSTNAME_TEMPLATE="{clusterName}.search-lb.lt.example.com:443"

# ============================================================================
# OPERATOR INSTALL
# ============================================================================

export OPERATOR_NAMESPACE="${MDB_NS}"
export OPERATOR_HELM_CHART="oci://quay.io/mongodb/helm-charts/mongodb-kubernetes"
export OPERATOR_ADDITIONAL_HELM_VALUES=""
