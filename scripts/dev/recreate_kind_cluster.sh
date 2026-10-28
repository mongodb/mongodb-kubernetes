#!/usr/bin/env bash

set -Eeou pipefail

source scripts/dev/set_env_context.sh
source scripts/funcs/kubernetes

: "${KIND_NETWORK_SUBNET:?not in the context, run make switch}" "${WORKTREE_PREFIX?not in the context, run make switch}"

cluster_name=$1
if [[ -z ${cluster_name} ]]; then
  echo "Usage: recreate_kind_cluster.sh <cluster_name>

Creates the kind cluster <WORKTREE_PREFIX><cluster_name>.
$(print_worktree_prefix_help)"
  exit 1
fi

# A worktree with a prefix shares the docker daemon, so it never deletes the network.
if [[ -z "${WORKTREE_PREFIX}" && "${DELETE_KIND_NETWORK:-"false"}" == "true" ]]; then
  refuse_to_delete_foreign_kind_clusters
  delete_kind_network
fi

docker_create_kind_network
docker_run_local_registry "kind-registry" "5000"

ecr_login_for_kind_node_image

real_name="${WORKTREE_PREFIX}${cluster_name}"
scripts/dev/setup_kind_cluster.sh -r -e -n "${real_name}" -l "${KIND_METALLB_RANGE_SINGLE}" -c "${CLUSTER_DOMAIN}"

source scripts/dev/install_csi_driver.sh
csi_driver_download
csi_driver_deploy "kind-${real_name}"

# The kubeconfig of a worktree with a prefix holds only this cluster, as kind-<cluster_name>.
if [[ -n "${WORKTREE_PREFIX}" ]]; then
  cp -f "${PROJECT_DIR}/.generated/kind-kubeconfigs/${real_name}" "${KUBECONFIG}"
  kind_unprefix_contexts "${KUBECONFIG}"
fi
