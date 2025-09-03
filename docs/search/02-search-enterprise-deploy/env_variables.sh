# set it to the context name of the k8s cluster
export K8S_CTX="<local cluster context>"

# the following namespace will be created if not exists
export MDB_NS="mongodb"

export MDB_RESOURCE_NAME="mdb-rs"

export OPS_MANAGER_PROJECT_NAME="${MDB_RESOURCE_NAME}"
# Ops Manager / Cloud Manager API configuration
export OPS_MANAGER_API_URL="<Base URL to Ops Manager instance, e.g. https://cloud-qa.mongodb.com>"
export OPS_MANAGER_API_USER="<Ops Manager's API user, e.g. abcdef12>"
export OPS_MANAGER_API_KEY="<Ops Manager's API key, e.g. 1234-231-2>"
export OPS_MANAGER_ORG_ID="<Ops Manager's Org ID>"

# minimum required MongoDB version for running MongoDB Search is 8.0.10
export MDB_VERSION="8.0.10"

# name of the MongoDB Custom Resource.

# root admin user for convenience, not used here at all in this guide
export MDB_ADMIN_USER_PASSWORD="admin-user-password-CHANGE-ME"
# regular user performing restore and search queries on sample mflix database
export MDB_USER_PASSWORD="mdb-user-password-CHANGE-ME"
# user for MongoDB Search to connect to the replica set to synchronise data from
export MDB_SEARCH_SYNC_USER_PASSWORD="search-sync-user-password-CHANGE-ME"


export OPERATOR_HELM_CHART="mongodb/mongodb-kubernetes"
# comma-separated key=value pairs for additional parameters passed to the helm-chart installing the operator
export OPERATOR_ADDITIONAL_HELM_VALUES=""

export MDB_CONNECTION_STRING="mongodb://mdb-user:${MDB_USER_PASSWORD}@${MDB_RESOURCE_NAME}-svc.${MDB_NS}.svc.cluster.local:27017/?replicaSet=mdbc-rs"
echo "new connection string = ${MDB_CONNECTION_STRING}"

