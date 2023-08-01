#!/usr/bin/env bash

set -Eeou pipefail

source scripts/dev/set_env_context.sh

UBI_IMAGE_SUFFIX=""

# some images here we set explicitly to quay here
UBI_IMAGE_SUFFIX_QUAY=""

# This is to set correct image names for ubuntu and ubi image types.
# We publish official UBI images to quay by adding "-ubi" suffix to the image name, e.g.:
#  ubuntu: quay.io/mongodb/mongodb-enterprise-init-database
#  ubi:    quay.io/mongodb/mongodb-enterprise-init-database-ubi
# But when we publish to our AWS dev registries we don't add suffixes to names, but publish them to:
#  ubuntu: 268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/ubuntu/mongodb-enterprise-init-database
#  ubi:    268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/ubi/mongodb-enterprise-init-database
if [[ "${IMAGE_TYPE}" == "ubi" ]]; then
  UBI_IMAGE_SUFFIX_QUAY="-ubi"

  if [[ "${UBI_IMAGE_WITHOUT_SUFFIX:-}" == "true" ]]; then
    UBI_IMAGE_SUFFIX=""
  else
    UBI_IMAGE_SUFFIX="-ubi"
  fi
fi


# Convert context variables to variables required by the operator binary
function print_operator_env() {
  echo "OPERATOR_ENV=\"$OPERATOR_ENV\"
WATCH_NAMESPACE=\"$WATCH_NAMESPACE\"
NAMESPACE=\"$NAMESPACE\"
IMAGE_PULL_POLICY=\"Always\"
MONGODB_ENTERPRISE_DATABASE_IMAGE=\"${DATABASE_REGISTRY}/mongodb-enterprise-database${UBI_IMAGE_SUFFIX}\"
INIT_DATABASE_IMAGE_REPOSITORY=\"${INIT_DATABASE_REGISTRY}/mongodb-enterprise-init-database${UBI_IMAGE_SUFFIX}\"
INIT_DATABASE_VERSION=\"${INIT_DATABASE_VERSION}\"
DATABASE_VERSION=\"${DATABASE_VERSION}\"
OPS_MANAGER_IMAGE_REPOSITORY=\"${OPS_MANAGER_REGISTRY}/mongodb-enterprise-ops-manager${UBI_IMAGE_SUFFIX}\"
INIT_OPS_MANAGER_IMAGE_REPOSITORY=\"${INIT_OPS_MANAGER_REGISTRY}/mongodb-enterprise-init-ops-manager${UBI_IMAGE_SUFFIX}\"
INIT_OPS_MANAGER_VERSION=\"${INIT_OPS_MANAGER_VERSION}\"
INIT_APPDB_IMAGE_REPOSITORY=\"${INIT_APPDB_REGISTRY}/mongodb-enterprise-init-appdb${UBI_IMAGE_SUFFIX}\"
INIT_APPDB_VERSION=\"${INIT_APPDB_VERSION}\"
OPS_MANAGER_IMAGE_PULL_POLICY=\"Always\"
AGENT_IMAGE=\"quay.io/mongodb/mongodb-agent${UBI_IMAGE_SUFFIX_QUAY}:${agent_version:-}\"
MONGODB_IMAGE=\"mongodb-enterprise-server\"
MONGODB_REPO_URL=\"quay.io/mongodb\"
IMAGE_PULL_SECRETS=\"image-registries-secret\""

if [[ "${KUBECONFIG:-""}" != "" ]]; then
  echo "KUBECONFIG=${KUBECONFIG}"
fi

if [[ "${KUBE_CONFIG_PATH:-""}" != "" ]]; then
  echo "KUBE_CONFIG_PATH=${KUBE_CONFIG_PATH}"
fi

if [[ "${PERFORM_FAILOVER:-""}" != "" ]]; then
  echo "PERFORM_FAILOVER=${PERFORM_FAILOVER}"
fi

}

print_operator_env