

# ======================================================================
# PREREQUISITES (from ra-01..ra-06 -- do not redefine if you already
# sourced those env_variables.sh files)
# ======================================================================

# Kubernetes contexts. K8S_CLUSTER_0 doubles as ra-02's "central" cluster
# (it hosts the sharded source below, deployed by THIS scenario) AND one
# of the two Search clusters; K8S_CLUSTER_1 is Search-only.
export K8S_CLUSTER_0_CONTEXT_NAME="<cluster-0 context>"
export K8S_CLUSTER_1_CONTEXT_NAME="<cluster-1 context>"

# Namespaces (ra-02's defaults)
export MDB_NAMESPACE="mongodb"
export OM_NAMESPACE="mongodb-om"
export OPERATOR_NAMESPACE="mongodb-operator"

# No OPS_MANAGER_API_* vars here: ra-06 (Ops Manager, deployed on
# K8S_CLUSTER_0) already created the `mdb-org-owner-credentials` Secret and
# `mdb-org-project-config` ConfigMap in ${MDB_NAMESPACE} that the Automation
# Config step (13_0330) derives its connection details from -- same pattern
# scenario 12's 12_0400 uses. See ra-06-ops-manager-multi-cluster/ra-06_0610.

# ======================================================================
# SHARDED SOURCE (this scenario deploys it -- it is NOT a prerequisite
# like ra-08. It is a single-cluster, operator-managed sharded MongoDB,
# same shape as docs/search/09, living entirely in K8S_CLUSTER_0.)
# ======================================================================

export MDB_RESOURCE_NAME="mdb-sh"
export MDB_VERSION="8.3.4-ent"

export MDB_SHARD_COUNT=2
export MDB_MONGODS_PER_SHARD=1
export MDB_MONGOS_COUNT=1
export MDB_CONFIG_SERVER_COUNT=2

# ======================================================================
# MONGODBSEARCH RESOURCE NAMING
# ======================================================================

export MDBS_RESOURCE_NAME="mdbs-sh"

# Per-cluster identity. spec.clusters[].name must equal the value each
# cluster's operator is started with (Helm operator.clusterIdentity.clusterName /
# env OPERATOR_CLUSTER_NAME). Reusing the kube context name means one
# fewer name to keep in sync; any stable string works.
export MDBS_CLUSTER_0_NAME="${K8S_CLUSTER_0_CONTEXT_NAME}"
export MDBS_CLUSTER_0_INDEX=0
export MDBS_CLUSTER_1_NAME="${K8S_CLUSTER_1_CONTEXT_NAME}"
export MDBS_CLUSTER_1_INDEX=1

# Which search cluster the source's mongod/mongos processes currently sync
# to (set on the OM Automation Config, not the CR). Flipping this value and
# re-running that one step is this scenario's operational lever -- see the README.
export TARGET_CLUSTER_INDEX="${MDBS_CLUSTER_0_INDEX}"

# Shard names must match what the source's operator generates: {MDB_RESOURCE_NAME}-{shardIdx}
export MDB_SHARD_0_NAME="${MDB_RESOURCE_NAME}-0"
export MDB_SHARD_1_NAME="${MDB_RESOURCE_NAME}-1"

# mongot replicas per (cluster, shard) cell. There is only ONE source, not
# one per search cluster, so every search cluster gets a FULL set of mongot
# groups for EVERY shard -- there's no per-cluster subset to assign.
export MDBS_MONGOT_REPLICAS=2
export MDBS_ENVOY_LB_REPLICAS=1

# ======================================================================
# TLS CONFIGURATION
# ======================================================================

export MDBS_TLS_CERT_SECRET_PREFIX="certs"
export MDB_TLS_CERT_SECRET_PREFIX="clustercert"

# One ConfigMap serves both the source MongoDB's own tls.ca (key ca-pem) and
# MongoDBSearch's spec.source.external.tls.ca (key ca.crt) -- same convention
# as create_issuer_ca in the e2e this scenario is modeled on.
export MDBS_TLS_CA_CONFIGMAP="${MDB_RESOURCE_NAME}-ca"

export CERT_MANAGER_NAMESPACE="cert-manager"
export MDB_TLS_SELF_SIGNED_ISSUER="selfsigned-bootstrap-issuer"
export MDB_TLS_CA_CERT_NAME="my-selfsigned-ca"
export MDB_TLS_CA_SECRET_NAME="root-secret"
export MDB_TLS_CA_ISSUER="my-ca-issuer"

# ======================================================================
# SYNC-SOURCE USER
# ======================================================================

export MDBS_SEARCH_SYNC_USER_PASSWORD="search-sync-user-password-CHANGE-ME"

# ======================================================================
# OPERATOR CONFIGURATION
# ======================================================================

HELM_REPO="oci://quay.io/mongodb/helm-charts"
export OPERATOR_HELM_CHART="${HELM_REPO}/mongodb-kubernetes"
# Distinct release name: K8S_CLUSTER_0 already runs ra-02's central operator
# in this namespace (managing the sharded source below); this is a SECOND,
# independent Helm release per cluster, watching only MongoDBSearch.
export SEARCH_OPERATOR_RELEASE_NAME="mongodb-kubernetes-operator-search"
export OPERATOR_ADDITIONAL_HELM_VALUES=""

# ======================================================================
# DERIVED VALUES (computed from topology + search config, do not change)
# ======================================================================

# --- Source hosts. Standard single-cluster sharded-cluster naming -- no
# cluster-index component, because the source only ever lives in ONE
# cluster (K8S_CLUSTER_0), never spread across the Search clusters. ---
MDB_SVC_SUFFIX="${MDB_NAMESPACE}.svc.cluster.local:27017"
export MDB_MONGOS_HOST_0="${MDB_RESOURCE_NAME}-mongos-0.${MDB_RESOURCE_NAME}-svc.${MDB_SVC_SUFFIX}"
export MDB_SHARD_0_HOST_0="${MDB_RESOURCE_NAME}-0-0.${MDB_RESOURCE_NAME}-sh.${MDB_SVC_SUFFIX}"
export MDB_SHARD_1_HOST_0="${MDB_RESOURCE_NAME}-1-0.${MDB_RESOURCE_NAME}-sh.${MDB_SVC_SUFFIX}"

# --- Per-cluster proxy endpoints (operator-derived names) ---
MDBS_SVC_SUFFIX="${MDB_NAMESPACE}.svc.cluster.local:27028"

# Shard-agnostic, per-cluster: the endpoint mongos routes to (routerHostname).
export MDBS_CLUSTER_0_ROUTER_HOSTNAME="${MDBS_RESOURCE_NAME}-search-${MDBS_CLUSTER_0_INDEX}-proxy-svc.${MDBS_SVC_SUFFIX}"
export MDBS_CLUSTER_1_ROUTER_HOSTNAME="${MDBS_RESOURCE_NAME}-search-${MDBS_CLUSTER_1_INDEX}-proxy-svc.${MDBS_SVC_SUFFIX}"

# Per-shard, per-cluster: the {shardName} template each cluster's Envoy expects
# for SNI matching (externalHostname). The operator substitutes {shardName}.
export MDBS_CLUSTER_0_EXTERNAL_HOSTNAME_TEMPLATE="${MDBS_RESOURCE_NAME}-search-${MDBS_CLUSTER_0_INDEX}-{shardName}-proxy-svc.${MDB_NAMESPACE}.svc.cluster.local"
export MDBS_CLUSTER_1_EXTERNAL_HOSTNAME_TEMPLATE="${MDBS_RESOURCE_NAME}-search-${MDBS_CLUSTER_1_INDEX}-{shardName}-proxy-svc.${MDB_NAMESPACE}.svc.cluster.local"
