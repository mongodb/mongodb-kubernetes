#!/usr/bin/env bash

set -Eeou pipefail

source scripts/dev/set_env_context.sh

cluster_name=$1
if [[ -z ${cluster_name} ]]; then
  echo "Usage: recreate_kind_cluster.sh <cluster_name>"
  exit 1
fi

scripts/dev/setup_kind_cluster.sh -r -e -n "${cluster_name}" -l "172.18.255.200-172.18.255.250" -c "$CLUSTER_DOMAIN"
CTX_CLUSTER1=${cluster_name}-kind scripts/dev/install_csi_driver.sh
