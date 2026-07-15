# This scenario builds on top of the multi-cluster reference architecture setup guides.
# It depends on (uses) the following env variables defined there to work correctly.
# If you don't use the setup guides to bootstrap the environment, then define them here.
#  ${K8S_CLUSTER_0_CONTEXT_NAME}          (public/architectures/setup-multi-cluster/ra-01-setup-gke)
#  ${K8S_CLUSTER_1_CONTEXT_NAME}
#  ${K8S_CLUSTER_2_CONTEXT_NAME}
#  ${MDB_NAMESPACE}                       (ra-02-setup-operator)
#  ${OM_NAMESPACE}                        (ra-02-setup-operator)
#  ${OPERATOR_HELM_CHART}                 (ra-02-setup-operator)
#  ${RS_RESOURCE_NAME}                    (ra-07-mongodb-replicaset-multi-cluster)
#
# ra-05-setup-cert-manager and ra-07-mongodb-replicaset-multi-cluster must also have
# run: this scenario reuses their CA (root-secret / my-ca-issuer in the cert-manager
# namespace on cluster 0) and their MongoDBMultiCluster replica set as the Search
# source. Neither is duplicated here -- see the README's Prerequisites section.

# ============================================================================
# CLUSTER IDENTITY (operator-per-cluster with a unified CR)
# ============================================================================
# Every member cluster runs its own MongoDB Kubernetes Operator instance, scoped to
# MongoDBSearch only via `operator.clusterIdentity.clusterName`. We reuse the kube
# context names as the cluster identities -- the same names ra-02's
# `kubectl mongodb multicluster setup` and ra-07's clusterSpecList already use, so
# every doc set in this chain agrees on what a "cluster name" is.
#
# IMPORTANT: cluster index N below must match the position of
# K8S_CLUSTER_N_CONTEXT_NAME in ra-07's clusterSpecList (ra-07_1100_mongodb_replicaset_multi_cluster.sh).
# The per-process mongotHost patch (12_0400) relies on this 1:1 mapping to avoid a
# separate cluster-name -> index translation table.
export SEARCH_CLUSTER_0_NAME="${K8S_CLUSTER_0_CONTEXT_NAME}"
export SEARCH_CLUSTER_0_INDEX=0
export SEARCH_CLUSTER_1_NAME="${K8S_CLUSTER_1_CONTEXT_NAME}"
export SEARCH_CLUSTER_1_INDEX=1
export SEARCH_CLUSTER_2_NAME="${K8S_CLUSTER_2_CONTEXT_NAME}"
export SEARCH_CLUSTER_2_INDEX=2

# Replica set member counts per cluster -- must match ra-07's clusterSpecList
# (ra-07_1100_mongodb_replicaset_multi_cluster.sh: members 2, 1, 2).
export RS_MEMBERS_CLUSTER_0=2
export RS_MEMBERS_CLUSTER_1=1
export RS_MEMBERS_CLUSTER_2=2

# ============================================================================
# SEARCH OPERATOR (installed once per member cluster, distinct from ra-02's
# central hub-and-spoke operator release)
# ============================================================================

export SEARCH_OPERATOR_RELEASE_NAME="mongodb-kubernetes-operator-search"
export SEARCH_OPERATOR_NAME="mongodb-kubernetes-operator-search"

# ============================================================================
# MONGODBSEARCH RESOURCE
# ============================================================================

# Applied identically (same name, same spec.clusters[]) to every member cluster.
# Deliberately does not contain "search" -- the operator appends "-search-<idx>-..."
# to this name for every resource it creates, and "mdb-mc-search-..." reads better
# than a doubled "mdb-search-search-...".
export SEARCH_RESOURCE_NAME="mdb-mc"

# mongot replicas per cluster. Replicas > 1 requires a load balancer, which is why
# this scenario always sets clusters[].loadBalancer.managed (see ClusterSpec.Replicas
# doc comment in api/mongodb/v1/search/mongodbsearch_types.go).
export SEARCH_MONGOT_REPLICAS=2

export SEARCH_ENVOY_LB_REPLICAS=1
export ENVOY_PROXY_PORT=27028

# ============================================================================
# SYNC-SOURCE USER (change the password in production!)
# ============================================================================

export SEARCH_SYNC_USER_NAME="search-sync-source"
export SEARCH_SYNC_USER_PASSWORD="search-sync-source-password-CHANGE-ME"

# Regular admin user for the functional verification steps (12_0500-12_0540):
# inserting sample data and running $search/$vectorSearch queries.
export SEARCH_ADMIN_USER_NAME="search-admin"
export SEARCH_ADMIN_USER_PASSWORD="search-admin-password-CHANGE-ME"

# ============================================================================
# TLS CONFIGURATION
# ============================================================================
# certsSecretPrefix on the MongoDBSearch CR. The mongot server cert, the per-cluster
# LB certs, and (indirectly) the source CA ConfigMap name below all derive from this.
export SEARCH_TLS_CERT_SECRET_PREFIX="certs"

# Shared with ra-05/ra-07: the CA cert-manager issues everything from.
export CERT_MANAGER_NAMESPACE="cert-manager"
export MDB_TLS_CA_SECRET_NAME="root-secret"
export MDB_TLS_CA_ISSUER="my-ca-issuer"

# ConfigMap (key ca.crt) that mongot trusts the source MongoDBMultiCluster's server
# cert against. Distinct from ra-05's "ca-issuer" ConfigMap, which carries ca-pem /
# mms-ca.crt keys but not ca.crt -- MongoDBSearch's source.external.tls.ca requires
# the ca.crt key specifically. Created fresh (same content) in every member cluster.
export SOURCE_CA_CONFIGMAP="${RS_RESOURCE_NAME}-search-source-ca"

# ============================================================================
# DERIVED VALUES (computed from topology + search config above)
# ============================================================================

# Every RS member's Service FQDN across all 3 clusters -- the seed list mongot uses
# to discover the source replica set. Naming: <RS_RESOURCE_NAME>-<clusterIdx>-<memberIdx>-svc
# (kubetester.mongodb_multi.MongoDBMulti.service_names() naming convention).
export SEARCH_SOURCE_SEED_0_0="${RS_RESOURCE_NAME}-0-0-svc.${MDB_NAMESPACE}.svc.cluster.local:27017"
export SEARCH_SOURCE_SEED_0_1="${RS_RESOURCE_NAME}-0-1-svc.${MDB_NAMESPACE}.svc.cluster.local:27017"
export SEARCH_SOURCE_SEED_1_0="${RS_RESOURCE_NAME}-1-0-svc.${MDB_NAMESPACE}.svc.cluster.local:27017"
export SEARCH_SOURCE_SEED_2_0="${RS_RESOURCE_NAME}-2-0-svc.${MDB_NAMESPACE}.svc.cluster.local:27017"
export SEARCH_SOURCE_SEED_2_1="${RS_RESOURCE_NAME}-2-1-svc.${MDB_NAMESPACE}.svc.cluster.local:27017"

# Per-cluster proxy-svc FQDN: the stable local endpoint each cluster's managed Envoy
# LB listens on, and what that cluster's mongod processes point mongotHost at.
# Naming: <SEARCH_RESOURCE_NAME>-search-<clusterIdx>-proxy-svc (ProxyServiceNamespacedNameForCluster).
export SEARCH_PROXY_SVC_0="${SEARCH_RESOURCE_NAME}-search-${SEARCH_CLUSTER_0_INDEX}-proxy-svc.${MDB_NAMESPACE}.svc.cluster.local"
export SEARCH_PROXY_SVC_1="${SEARCH_RESOURCE_NAME}-search-${SEARCH_CLUSTER_1_INDEX}-proxy-svc.${MDB_NAMESPACE}.svc.cluster.local"
export SEARCH_PROXY_SVC_2="${SEARCH_RESOURCE_NAME}-search-${SEARCH_CLUSTER_2_INDEX}-proxy-svc.${MDB_NAMESPACE}.svc.cluster.local"

# Replica-set connection string for the functional verification steps, used from the
# mongodb-tools pods (12_0510), which mount the source CA at /tls/ca.crt.
export MDB_CONNECTION_STRING="mongodb://${SEARCH_ADMIN_USER_NAME}:${SEARCH_ADMIN_USER_PASSWORD}@${SEARCH_SOURCE_SEED_0_0},${SEARCH_SOURCE_SEED_0_1},${SEARCH_SOURCE_SEED_1_0},${SEARCH_SOURCE_SEED_2_0},${SEARCH_SOURCE_SEED_2_1}/?replicaSet=${RS_RESOURCE_NAME}&authSource=admin&tls=true&tlsCAFile=/tls/ca.crt"
