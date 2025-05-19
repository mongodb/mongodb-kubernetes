# set it to the context name of the k8s cluster
export K8S_CLUSTER_0_CONTEXT_NAME="<local cluster context>"

# At the private preview stage the community search image is accessible only from a private repository.
# Please contact MongoDB Support to get access.
export PRIVATE_PREVIEW_IMAGE_PULLSECRET="<.dockerconfigjson>"

# the following namespace will be created if not exists
export MDB_NAMESPACE="mongodb"

export MDB_ADMIN_USER_PASSWORD="admin-user-password-CHANGE-ME"
export MDB_SEARCH_USER_PASSWORD="search-user-password-CHANGE-ME"

export OPERATOR_HELM_CHART="mongodb/mongodb-kubernetes"
# comma-separated key=value pairs for additional parameters passed to the helm-chart installing the operator
export OPERATOR_ADDITIONAL_HELM_VALUES=""
