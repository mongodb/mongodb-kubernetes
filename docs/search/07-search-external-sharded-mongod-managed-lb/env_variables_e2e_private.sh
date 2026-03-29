# E2E Test Environment Overrides (Enterprise with Ops Manager)
#
# This file is sourced by the test runner to override environment variables
# for automated E2E testing. Do not use this file for manual testing.
# NOTE: This uses the enterprise context with cloud-qa Ops Manager.

source "${PROJECT_DIR}/scripts/funcs/operator_deployment"
source "${PROJECT_DIR}/scripts/dev/contexts/e2e_mdb_kind_ubi_cloudqa"

# K8S_CTX must be set after sourcing e2e_mdb_kind_ubi_cloudqa which sets CLUSTER_NAME
export K8S_CTX="${CLUSTER_NAME}"

OPERATOR_ADDITIONAL_HELM_VALUES="$(get_operator_helm_values | tr ' ' ',')"
export OPERATOR_ADDITIONAL_HELM_VALUES
export OPERATOR_HELM_CHART="${PROJECT_DIR}/helm_chart"

# Simulated external cluster name (used by internal scripts only)
export MDB_EXTERNAL_CLUSTER_NAME="ext-mdb-sh"
export MDB_TLS_CA_CONFIGMAP="${MDB_EXTERNAL_CLUSTER_NAME}-ca"

# Shard topology for simulated external cluster
export MDB_EXTERNAL_SHARD_0_NAME="ext-mdb-sh-0"
export MDB_EXTERNAL_SHARD_0_HOST="${MDB_EXTERNAL_CLUSTER_NAME}-0-0.${MDB_EXTERNAL_DOMAIN}:27017"
export MDB_EXTERNAL_SHARD_1_NAME="ext-mdb-sh-1"
export MDB_EXTERNAL_SHARD_1_HOST="${MDB_EXTERNAL_CLUSTER_NAME}-1-0.${MDB_EXTERNAL_DOMAIN}:27017"
export MDB_EXTERNAL_MONGOS_HOST="${MDB_EXTERNAL_CLUSTER_NAME}-mongos-0.${MDB_EXTERNAL_DOMAIN}:27017"

# Connection strings
MDB_TLS_OPTS="tls=true&tlsCAFile=/tls/ca-pem"
MDB_AUTH_OPTS="authSource=admin&authMechanism=SCRAM-SHA-256"
MDB_CONN_OPTS="?${MDB_TLS_OPTS}&${MDB_AUTH_OPTS}"
export MDB_ADMIN_CONNECTION_STRING="mongodb://mdb-admin:${MDB_ADMIN_USER_PASSWORD}@${MDB_EXTERNAL_MONGOS_HOST}/${MDB_CONN_OPTS}"
export MDB_USER_CONNECTION_STRING="mongodb://mdb-user:${MDB_USER_PASSWORD}@${MDB_EXTERNAL_MONGOS_HOST}/${MDB_CONN_OPTS}"

# we need project name with a timestamp (NAMESPACE in evg is randomized) to allow for cloud-qa cleanups
export OPS_MANAGER_PROJECT_NAME="${NAMESPACE}-${MDB_EXTERNAL_CLUSTER_NAME}"
export OPS_MANAGER_API_URL="${OM_BASE_URL}"
export OPS_MANAGER_API_USER="${OM_USER}"
export OPS_MANAGER_API_KEY="${OM_API_KEY}"
export OPS_MANAGER_ORG_ID="${OM_ORGID}"
