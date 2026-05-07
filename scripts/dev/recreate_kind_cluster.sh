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

# shellcheck source=../funcs/kind_network
source scripts/funcs/kind_network
scripts/dev/setup_kind_cluster.sh -r -e -n "${cluster_name}" -l "${KIND_METALLB_RANGE_SINGLE}" -c "${CLUSTER_DOMAIN}"

source scripts/dev/install_csi_driver.sh
csi_driver_download
csi_driver_deploy "kind-${cluster_name}"

# Stamp the canonical per-side kubeconfig path so downstream tooling has
# one source of truth for "kind cluster was just created on this host" —
# the same artifact whether this runs on a laptop (local-kind) or an EVG
# runner (EVG-CI). Mirrors recreate_kind_clusters.sh's tail.
mkdir -p .generated
cp -f "${HOME}/.kube/${cluster_name}" .generated/current.kubeconfig
