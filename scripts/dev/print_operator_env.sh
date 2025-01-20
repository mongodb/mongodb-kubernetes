#!/usr/bin/env bash

set -Eeou pipefail

# NOTE: these are the env vars which are required to run the operator, either via a pod or locally

UBI_IMAGE_SUFFIX="-ubi"

# Convert context variables to variables required by the operator binary
function print_operator_env() {
  echo "OPERATOR_ENV=\"${OPERATOR_ENV}\"
WATCH_NAMESPACE=\"${WATCH_NAMESPACE}\"
NAMESPACE=\"${NAMESPACE}\"
IMAGE_PULL_POLICY=\"Always\"
MONGODB_ENTERPRISE_DATABASE_IMAGE=\"${MONGODB_ENTERPRISE_DATABASE_IMAGE:-${DATABASE_REGISTRY}/mongodb-enterprise-database${UBI_IMAGE_SUFFIX}}\"
INIT_DATABASE_IMAGE_REPOSITORY=\"${INIT_DATABASE_REGISTRY}/mongodb-enterprise-init-database${UBI_IMAGE_SUFFIX}\"
INIT_DATABASE_VERSION=\"${INIT_DATABASE_VERSION}\"
DATABASE_VERSION=\"${DATABASE_VERSION}\"
OPS_MANAGER_IMAGE_REPOSITORY=\"${OPS_MANAGER_REGISTRY}/mongodb-enterprise-ops-manager${UBI_IMAGE_SUFFIX}\"
INIT_OPS_MANAGER_IMAGE_REPOSITORY=\"${INIT_OPS_MANAGER_REGISTRY}/mongodb-enterprise-init-ops-manager${UBI_IMAGE_SUFFIX}\"
INIT_OPS_MANAGER_VERSION=\"${INIT_OPS_MANAGER_VERSION}\"
INIT_APPDB_IMAGE_REPOSITORY=\"${INIT_APPDB_REGISTRY}/mongodb-enterprise-init-appdb${UBI_IMAGE_SUFFIX}\"
INIT_APPDB_VERSION=\"${INIT_APPDB_VERSION}\"
OPS_MANAGER_IMAGE_PULL_POLICY=\"Always\"
MONGODB_IMAGE=\"mongodb-enterprise-server\"
MONGODB_AGENT_VERSION=\"${MONGODB_AGENT_VERSION:-}\"
MONGODB_REPO_URL=\"${MONGODB_REPO_URL:-}\"
IMAGE_PULL_SECRETS=\"image-registries-secret\"
MDB_DEFAULT_ARCHITECTURE=\"${MDB_DEFAULT_ARCHITECTURE:-non-static}\"
MDB_IMAGE_TYPE=\"${MDB_IMAGE_TYPE:-ubi8}\"
"

if [[ "${AGENT_IMAGE:-}" != "" ]]; then
  echo "AGENT_IMAGE=${AGENT_IMAGE}"
else
  echo "AGENT_IMAGE=\"quay.io/mongodb/mongodb-agent${UBI_IMAGE_SUFFIX}:${AGENT_VERSION:-}\""
fi

if [[ "${KUBECONFIG:-""}" != "" ]]; then
  echo "KUBECONFIG=${KUBECONFIG}"
fi

if [[ "${MDB_AGENT_VERSION:-""}" != "" ]]; then
  echo "MDB_AGENT_VERSION=${MDB_AGENT_VERSION}"
fi

if [[ "${MDB_AGENT_DEBUG:-""}" != "" ]]; then
  echo "MDB_AGENT_DEBUG=${MDB_AGENT_DEBUG}"
fi

if [[ "${KUBE_CONFIG_PATH:-""}" != "" ]]; then
  echo "KUBE_CONFIG_PATH=${KUBE_CONFIG_PATH}"
fi

if [[ "${PERFORM_FAILOVER:-""}" != "" ]]; then
  echo "PERFORM_FAILOVER=${PERFORM_FAILOVER}"
fi

if [[ "${OM_DEBUG_HTTP:-""}" != "" ]]; then
  echo "OM_DEBUG_HTTP=${OM_DEBUG_HTTP}"
fi

if [[ "${OPS_MANAGER_MONITOR_APPDB:-""}" != "" ]]; then
  echo "OPS_MANAGER_MONITOR_APPDB=${OPS_MANAGER_MONITOR_APPDB}"
fi

if [[ "${OPERATOR_ENV:-""}" != "" ]]; then
  echo "OPERATOR_ENV=${OPERATOR_ENV}"
fi

if [[ "${MDB_OM_VERSION_MAPPING_PATH:-""}" != "" ]]; then
  echo "MDB_OM_VERSION_MAPPING_PATH=${MDB_OM_VERSION_MAPPING_PATH}"
fi

if [[ "${MDB_AGENT_IMAGE_REPOSITORY:-""}" != "" ]]; then
  echo "MDB_AGENT_IMAGE_REPOSITORY=${MDB_AGENT_IMAGE_REPOSITORY}"
fi

if [[ "${MDB_MAX_CONCURRENT_RECONCILES:-""}" != "" ]]; then
  echo "MDB_MAX_CONCURRENT_RECONCILES=${MDB_MAX_CONCURRENT_RECONCILES}"
fi


}

print_operator_env
