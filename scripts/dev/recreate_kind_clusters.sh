#!/usr/bin/env bash
set -Eeou pipefail

test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

source scripts/dev/set_env_context.sh
source scripts/funcs/kubernetes
source scripts/funcs/printing
source scripts/funcs/kind_network

# Scoped mode (MCK_DEVC_LOCAL_KIND=1): delete only the clusters this script creates and
# prune nothing, because the docker host can run clusters and containers that belong to
# somebody else.
docker_cleanup_scoped() {
  local name
  for name in "e2e-operator" "e2e-cluster-1" "e2e-cluster-2" "e2e-cluster-3" "kind"; do
    echo "Deleting kind cluster ${name}"
    kind delete cluster --name "${name}"
  done
}

docker_cleanup() {
  echo "Deleting all kind clusters"
  kind delete clusters --all
  echo "Pruning all docker resources"
  docker system prune --all --volumes --force

  if [[ "${DELETE_KIND_NETWORK:-"false"}" == "true" ]]; then
    delete_kind_network
  fi
}

if [[ "${MCK_DEVC_LOCAL_KIND:-}" == "1" ]]; then
  docker_cleanup_scoped 2>&1 | prepend "docker_cleanup"
else
  docker_cleanup 2>&1 | prepend "docker_cleanup"
fi

docker_create_kind_network
docker_run_local_registry "kind-registry" "5000"

# One login for the node image pulls of the parallel cluster creations below.
ecr_login_for_kind_node_image 2>&1 | prepend "ecr_login"

# To future maintainers: whenever modifying this bit, make sure you also update coredns.yaml
cluster_pids=()
(scripts/dev/setup_kind_cluster.sh -n "e2e-operator" -p "10.244.0.0/16" -s "10.96.0.0/16" -l "${KIND_METALLB_RANGE_OPERATOR}" -c "${CLUSTER_DOMAIN}" 2>&1 | prepend "e2e-operator") &
cluster_pids+=($!)
(scripts/dev/setup_kind_cluster.sh -n "e2e-cluster-1" -p "10.245.0.0/16" -s "10.97.0.0/16" -l "${KIND_METALLB_RANGE_CLUSTER_1}" -c "${CLUSTER_DOMAIN}" 2>&1 | prepend "e2e-cluster-1") &
cluster_pids+=($!)
(scripts/dev/setup_kind_cluster.sh -n "e2e-cluster-2" -p "10.246.0.0/16" -s "10.98.0.0/16" -l "${KIND_METALLB_RANGE_CLUSTER_2}" -c "${CLUSTER_DOMAIN}" 2>&1 | prepend "e2e-cluster-2") &
cluster_pids+=($!)
(scripts/dev/setup_kind_cluster.sh -n "e2e-cluster-3" -p "10.247.0.0/16" -s "10.99.0.0/16" -l "${KIND_METALLB_RANGE_CLUSTER_3}" -c "${CLUSTER_DOMAIN}" 2>&1 | prepend "e2e-cluster-3") &
cluster_pids+=($!)
(scripts/dev/setup_kind_cluster.sh -n "kind" -l "${KIND_METALLB_RANGE_KIND}" -c "${CLUSTER_DOMAIN}" 2>&1 | prepend "kind") &
cluster_pids+=($!)

echo "Waiting for all kind clusters to be created"
for pid in "${cluster_pids[@]}"; do wait "${pid}" || { echo "Cluster creation PID ${pid} failed" >&2; exit 1; }; done

# we do exports sequentially as setup_kind_cluster.sh is run in parallel and we hit kube config locks
kind export kubeconfig --name "e2e-operator"
kind export kubeconfig --name "e2e-cluster-1"
kind export kubeconfig --name "e2e-cluster-2"
kind export kubeconfig --name "e2e-cluster-3"
kind export kubeconfig --name "kind"

echo "Interconnecting Kind clusters"
scripts/dev/interconnect_kind_clusters.sh -v e2e-cluster-1 e2e-cluster-2 e2e-cluster-3 e2e-operator 2>&1 | prepend "interconnect_kind_clusters"

export VERSION=${VERSION:-1.16.1}

source multi_cluster/tools/download_istio.sh 2>&1 | prepend "download_istio"

istio_pids=()
VERSION=1.16.1 CTX_CLUSTER1=kind-e2e-cluster-1 CTX_CLUSTER2=kind-e2e-cluster-2 CTX_CLUSTER3=kind-e2e-cluster-3 multi_cluster/tools/install_istio.sh 2>&1 | prepend "install_istio" &
istio_pids+=($!)
VERSION=1.16.1 CTX_CLUSTER=kind-e2e-operator multi_cluster/tools/install_istio_central.sh 2>&1 | prepend "install_istio_central" &
istio_pids+=($!)

for pid in "${istio_pids[@]}"; do wait "${pid}" || { echo "Istio install PID ${pid} failed" >&2; exit 1; }; done

source scripts/dev/install_csi_driver.sh
csi_driver_download 2>&1 | prepend "csi_driver_download"

csi_pids=()
csi_driver_deploy kind-e2e-operator 2>&1 | prepend "install_csi_driver.sh kind-e2e-operator" &
csi_pids+=($!)
csi_driver_deploy kind-e2e-cluster-1 2>&1 | prepend "install_csi_driver.sh kind-e2e-cluster-1" &
csi_pids+=($!)
csi_driver_deploy kind-e2e-cluster-2 2>&1 | prepend "install_csi_driver.sh kind-e2e-cluster-2" &
csi_pids+=($!)
csi_driver_deploy kind-e2e-cluster-3 2>&1 | prepend "install_csi_driver.sh kind-e2e-cluster-3" &
csi_pids+=($!)
csi_driver_deploy kind-kind 2>&1 | prepend "install_csi_driver.sh kind-kind" &
csi_pids+=($!)

for pid in "${csi_pids[@]}"; do wait "${pid}" || { echo "CSI deploy PID ${pid} failed" >&2; exit 1; }; done
