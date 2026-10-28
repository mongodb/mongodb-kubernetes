#!/usr/bin/env bash

set -Eeou pipefail

source scripts/dev/set_env_context.sh
source scripts/funcs/kubernetes
source scripts/funcs/kind_network

cluster_name=$1
if [[ -z ${cluster_name} ]]; then
  echo "Usage: recreate_kind_cluster.sh <cluster_name>"
  exit 1
fi

# With a stack index (MCK_DEVC_HOST_INDEX, one of several kind setups on a docker host) the
# cluster is called devc<N>-<cluster_name> and sits on the stack's own docker network; the
# kubeconfig keeps the kind-<cluster_name> context. Only scoped mode is safe then.
kind_require_scoped_for_stack_index
real_cluster_name="${KIND_NAME_PREFIX}${cluster_name}"

# Scoped mode (MCK_DEVC_LOCAL_KIND=1) never deletes the shared 'kind' docker network, which
# stops every container on it. evg-private-context exports DELETE_KIND_NETWORK=true, so the
# override has to be here. set_env_context.sh pins KUBECONFIG to the checkout's file in that
# mode, so `kind export` leaves the user's ~/.kube/config alone.
if [[ "${MCK_DEVC_LOCAL_KIND:-}" == "1" ]]; then
  DELETE_KIND_NETWORK="false"
fi

if [[ "${DELETE_KIND_NETWORK:-"false"}" == "true" ]]; then
  delete_kind_network
fi

docker_create_kind_network
docker_run_local_registry "kind-registry" "5000"

ecr_login_for_kind_node_image

scripts/dev/setup_kind_cluster.sh -r -e -n "${real_cluster_name}" -l "${KIND_METALLB_RANGE_SINGLE}" -c "${CLUSTER_DOMAIN}"

source scripts/dev/install_csi_driver.sh
csi_driver_download
csi_driver_deploy "kind-${real_cluster_name}"

# kind export wrote the prefixed names into the stack's kubeconfig. Replace it with this
# cluster's own file under the usual kind-<cluster_name> context.
if [[ -n "${KIND_NAME_PREFIX}" ]]; then
  cp -f "$(kind_cluster_kubeconfig "${real_cluster_name}")" "${KUBECONFIG}"
  kind_logical_kubeconfig "${KUBECONFIG}"
fi
