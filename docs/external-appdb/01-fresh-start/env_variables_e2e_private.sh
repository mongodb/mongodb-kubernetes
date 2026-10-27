# E2E Test Environment Overrides (Enterprise with in-cluster Ops Manager)
#
# Sourced by the CI runner to override environment for automated E2E testing.
# Do not use this file for manual testing.

source "${PROJECT_DIR}/scripts/funcs/operator_deployment"
source "${PROJECT_DIR}/scripts/dev/contexts/e2e_mdb_kind_ubi_cloudqa"

# K8S_CTX must be set after sourcing the context which sets CLUSTER_NAME
export K8S_CTX="${CLUSTER_NAME}"

OPERATOR_ADDITIONAL_HELM_VALUES="$(get_operator_helm_values | tr ' ' ',')"
export OPERATOR_ADDITIONAL_HELM_VALUES
export OPERATOR_HELM_CHART="${PROJECT_DIR}/helm_chart"

source "$(dirname "${BASH_SOURCE[0]}")/env_variables_e2e_common.sh"
