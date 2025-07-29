export K8S_CLUSTER_0_CONTEXT_NAME="${CLUSTER_NAME}"

<<<<<<< Updated upstream
source "${PROJECT_DIR}/scripts/funcs/operator_deployment"
source "${PROJECT_DIR}/scripts/dev/contexts/e2e_mdb_community"
=======
source scripts/funcs/operator_deployment
>>>>>>> Stashed changes
OPERATOR_ADDITIONAL_HELM_VALUES="$(get_operator_helm_values | tr ' ' ',')"
export OPERATOR_ADDITIONAL_HELM_VALUES
export OPERATOR_HELM_CHART="${PROJECT_DIR}/helm_chart"
