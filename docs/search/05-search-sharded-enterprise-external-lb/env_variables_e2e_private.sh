source "${PROJECT_DIR}/scripts/funcs/operator_deployment"
source "${PROJECT_DIR}/scripts/dev/contexts/e2e_mdb_kind_ubi_cloudqa"

# K8S_CTX must be set after sourcing e2e_mdb_kind_ubi_cloudqa which sets CLUSTER_NAME
export K8S_CTX="${CLUSTER_NAME}"

# Override operator version to use a specific patch ID
# Set OVERRIDE_VERSION_ID before sourcing this file to use a specific operator image
# Example: export OVERRIDE_VERSION_ID="6970e10a7062fd0007d58e32"
if [[ -n "${OVERRIDE_VERSION_ID:-}" ]]; then
  export OPERATOR_VERSION="${OVERRIDE_VERSION_ID}"
fi

# Override init versions to use released versions (not the operator patch ID)
# The init-database image version should match a released version
export INIT_DATABASE_VERSION="latest"
export INIT_APPDB_VERSION="latest"
export INIT_OPS_MANAGER_VERSION="latest"
export DATABASE_VERSION="latest"

OPERATOR_ADDITIONAL_HELM_VALUES="$(get_operator_helm_values | tr ' ' ',')"
export OPERATOR_ADDITIONAL_HELM_VALUES
export OPERATOR_HELM_CHART="${PROJECT_DIR}/helm_chart"

# we need project name with a timestamp (NAMESPACE in evg is randomized) to allow for cloud-qa cleanups
export OPS_MANAGER_PROJECT_NAME="${NAMESPACE}-${MDB_RESOURCE_NAME}"
export OPS_MANAGER_API_URL="${OM_BASE_URL}"
export OPS_MANAGER_API_USER="${OM_USER}"
export OPS_MANAGER_API_KEY="${OM_API_KEY}"
export OPS_MANAGER_ORG_ID="${OM_ORGID}"
