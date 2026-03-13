# E2E Test Environment Overrides
#
# This file is sourced by the test runner to override environment variables
# for automated E2E testing. Do not use this file for manual testing.

export K8S_CTX="${CLUSTER_NAME}"
source "${PROJECT_DIR}/scripts/funcs/operator_deployment"
source "${PROJECT_DIR}/scripts/dev/contexts/e2e_mdb_community"
OPERATOR_ADDITIONAL_HELM_VALUES="$(get_operator_helm_values | tr ' ' ',')"
export OPERATOR_ADDITIONAL_HELM_VALUES
export OPERATOR_HELM_CHART="${PROJECT_DIR}/helm_chart"

