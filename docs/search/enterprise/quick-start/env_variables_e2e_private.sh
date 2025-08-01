export K8S_CLUSTER_0_CONTEXT_NAME="${CLUSTER_NAME}"

source "${PROJECT_DIR}/scripts/funcs/operator_deployment"
source "${PROJECT_DIR}/scripts/dev/contexts/e2e_mdb_kind_ubi_cloudqa"
OPERATOR_ADDITIONAL_HELM_VALUES="$(get_operator_helm_values | tr ' ' ',')"
export OPERATOR_ADDITIONAL_HELM_VALUES
export OPERATOR_HELM_CHART="${PROJECT_DIR}/helm_chart"

export MDB_NAMESPACE="${NAMESPACE}"
export MDB_OPS_MANAGER_CONFIG_MAP_NAME="my-project"
export MDB_OPS_MANAGER_CREDENTIALS_SECRET_NAME="my-credentials"
