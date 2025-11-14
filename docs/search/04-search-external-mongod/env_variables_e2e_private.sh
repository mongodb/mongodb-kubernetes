export K8S_CTX="${CLUSTER_NAME}"
source "${PROJECT_DIR}/scripts/funcs/operator_deployment"
source "${PROJECT_DIR}/scripts/dev/contexts/e2e_mdb_community"
OPERATOR_ADDITIONAL_HELM_VALUES="$(get_operator_helm_values | tr ' ' ',')"
export OPERATOR_ADDITIONAL_HELM_VALUES

get_helm_chart_build_info "${BUILD_SCENARIO}" "${OPERATOR_VERSION}"
export OPERATOR_HELM_CHART="${HELM_OCI_REPOSITORY}"
export OPERATOR_HELM_CHART_VERSION="${HELM_OCI_VERSION}"
