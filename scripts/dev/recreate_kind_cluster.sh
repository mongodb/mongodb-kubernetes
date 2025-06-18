#!/usr/bin/env bash

set -Eeou pipefail

source scripts/dev/set_env_context.sh
source scripts/funcs/kubernetes

cluster_name=$1
if [[ -z ${cluster_name} ]]; then
  echo "Usage: recreate_kind_cluster.sh <cluster_name>"
  exit 1
fi

if [[ "${DELETE_KIND_NETWORK:-"false"}" == "true" ]]; then
  delete_kind_network
fi

docker_create_kind_network
docker_run_local_registry "kind-registry" "5000"

create_audit_policy_yaml "${K8S_AUDIT_LOG_LEVEL}"

scripts/dev/setup_kind_cluster.sh -r -e -n "${cluster_name}" -l "172.18.255.200-172.18.255.250" -c "${CLUSTER_DOMAIN}"

source scripts/dev/install_csi_driver.sh
csi_driver_download
csi_driver_deploy "${cluster_name}-kind"
