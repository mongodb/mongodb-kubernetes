# Environment variables for MongoDB Search with External Sharded MongoDB Source
#
# This configuration is for deploying MongoDB Search against an external sharded MongoDB cluster.
# For testing purposes, we simulate an external cluster by deploying a MongoDB Enterprise
# sharded cluster in the same Kubernetes cluster, then configuring MongoDBSearch to treat it
# as an external source.

# set it to the context name of the k8s cluster
export K8S_CTX="<local cluster context>"

# the following namespace will be created if not exists
export MDB_NS="mongodb"

# Name for the simulated external MongoDB sharded cluster
export MDB_EXTERNAL_CLUSTER_NAME="external-mdb-sh"

# MongoDB Search resource name
export MDB_SEARCH_RESOURCE_NAME="mdbs-external-sharded"

# Sharded cluster configuration for the simulated external cluster
export MDB_SHARD_COUNT=2
export MDB_MONGODS_PER_SHARD=1
export MDB_MONGOS_COUNT=1
export MDB_CONFIG_SERVER_COUNT=2

# Number of mongot replicas per shard (default: 1)
# When > 1, multiple mongot pods are deployed per shard for high availability
export MDB_MONGOT_REPLICAS=1

# OM/CM's project name to be used to manage mongodb sharded cluster
export OPS_MANAGER_PROJECT_NAME="<arbitrary project name>"

# URL to Cloud Manager or Ops Manager instance
export OPS_MANAGER_API_URL="https://cloud-qa.mongodb.com"

# The API key can be an Org Owner - the operator can create the project automatically then.
# The API key can also be created in a particular project that was created manually with the Project Owner scope.
export OPS_MANAGER_API_USER="<SET API USER>"
export OPS_MANAGER_API_KEY="<SET API KEY>"
export OPS_MANAGER_ORG_ID="<SET ORG ID>"

# minimum required MongoDB version for running MongoDB Search is 8.2.0
export MDB_VERSION="8.2.0-ent"

# root admin user for convenience
export MDB_ADMIN_USER_PASSWORD="admin-user-password-CHANGE-ME"
# regular user performing restore and search queries on sample mflix database
export MDB_USER_PASSWORD="mdb-user-password-CHANGE-ME"
# user for MongoDB Search to connect to the replica set to synchronise data from
export MDB_SEARCH_SYNC_USER_PASSWORD="search-sync-user-password-CHANGE-ME"

export OPERATOR_HELM_CHART="mongodb/mongodb-kubernetes"
# comma-separated key=value pairs for additional parameters passed to the helm-chart installing the operator
export OPERATOR_ADDITIONAL_HELM_VALUES=""

# TLS configuration
export MDB_TLS_ENABLED="true"
export MDB_TLS_CERT_SECRET_PREFIX="certs"
export MDB_TLS_CA_CONFIGMAP="${MDB_EXTERNAL_CLUSTER_NAME}-ca"

export CERT_MANAGER_NAMESPACE="cert-manager"
export MDB_TLS_SELF_SIGNED_ISSUER="selfsigned-bootstrap-issuer"
export MDB_TLS_CA_CERT_NAME="my-selfsigned-ca"
export MDB_TLS_CA_SECRET_NAME="root-secret"
export MDB_TLS_CA_ISSUER="my-ca-issuer"
export MDB_SEARCH_TLS_SECRET_NAME="${MDB_SEARCH_RESOURCE_NAME}-search-tls"

# Connection string for mongos (sharded cluster entry point) with TLS
export MDB_CONNECTION_STRING="mongodb://mdb-user:${MDB_USER_PASSWORD}@${MDB_EXTERNAL_CLUSTER_NAME}-mongos-0.${MDB_EXTERNAL_CLUSTER_NAME}-svc.${MDB_NS}.svc.cluster.local:27017/?tls=true&tlsCAFile=/tls/ca.crt"

# Envoy proxy configuration
# Envoy acts as an L7 proxy between mongod and mongot for SNI-based routing
export ENVOY_IMAGE="envoyproxy/envoy:v1.31-latest"
export ENVOY_PROXY_PORT="27029"
