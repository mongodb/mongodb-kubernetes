export K8S_CTX="<your kubernetes context here>"

export MDB_NS="mongodb"

export MDB_VERSION="8.2.0"

export MDB_ADMIN_USER_PASSWORD="admin-user-password-CHANGE-ME"
export MDB_USER_PASSWORD="mdb-user-password-CHANGE-ME"
export MDB_SEARCH_SYNC_USER_PASSWORD="search-sync-user-password-CHANGE-ME"

export MDB_TLS_CA_SECRET_NAME="ca"
export MDB_SEARCH_TLS_SECRET_NAME="mdbs-search-tls"

export MDB_SEARCH_SERVICE_NAME="mdbs-search"
export MDB_SEARCH_HOSTNAME="mdbs-search.example.com"

# External MongoDB replica set members - REPLACE THESE VALUES with your actual external MongoDB hosts
# In production, replace with your actual external MongoDB replica set members
export MDB_EXTERNAL_HOST_0="mdbc-rs-0.mdbc-rs-svc.${MDB_NS}.svc.cluster.local:27017"
export MDB_EXTERNAL_HOST_1="mdbc-rs-1.mdbc-rs-svc.${MDB_NS}.svc.cluster.local:27017"
export MDB_EXTERNAL_HOST_2="mdbc-rs-2.mdbc-rs-svc.${MDB_NS}.svc.cluster.local:27017"

# REPLACE with your actual external MongoDB replica set name
export MDB_EXTERNAL_REPLICA_SET_NAME="mdbc-rs"

export OPERATOR_HELM_CHART="oci://quay.io/mongodb/helm-charts/mongodb-kubernetes"
# specify operator version or leave empty to install the latest available
export OPERATOR_HELM_CHART_VERSION=""
# comma-separated key=value pairs for additional parameters passed to the helm-chart installing the operator
export OPERATOR_ADDITIONAL_HELM_VALUES=""

export MDB_CONNECTION_STRING="mongodb://mdb-user:${MDB_USER_PASSWORD}@${MDB_EXTERNAL_HOST_0}/?replicaSet=${MDB_EXTERNAL_REPLICA_SET_NAME}&tls=true&tlsCAFile=/tls/ca.crt"
