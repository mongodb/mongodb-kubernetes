# set it to the context name of the k8s cluster
export K8S_CLUSTER_0_CONTEXT_NAME="<local cluster context>"

# the following namespace will be created if not exists
export MDB_NAMESPACE="mongodb"

# minimum required MongoDB version for running MongoDB Search is 8.0.10
export MDB_VERSION="8.0.10"

# root admin user for restoring the database from a sample backup
export MDB_ADMIN_USER_PASSWORD="admin-user-password-CHANGE-ME"
# regular user performing search queries on sample mflix database
export MDB_USER_PASSWORD="mdb-user-password-CHANGE-ME"
# user for MongoDB Search to connect to the replica set to synchronise data from
export MDB_SEARCH_SYNC_USER_PASSWORD="search-sync-user-password-CHANGE-ME"

export MDB_OPS_MANAGER_CONFIG_MAP_NAME="<Ops Manager project configmap name>"
export MDB_OPS_MANAGER_CREDENTIALS_SECRET_NAME="<Ops Manager credentials secret name>"

export OPERATOR_HELM_CHART="mongodb/mongodb-kubernetes"
# comma-separated key=value pairs for additional parameters passed to the helm-chart installing the operator
export OPERATOR_ADDITIONAL_HELM_VALUES=""
