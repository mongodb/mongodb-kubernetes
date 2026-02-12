#!/usr/bin/env bash
set -Eeou pipefail

test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

source scripts/dev/set_env_context.sh
source scripts/funcs/kubernetes
source scripts/funcs/printing

docker_cleanup() {
  echo "Deleting all kind clusters"
  kind delete clusters --all
  echo "Pruning all docker resources"
  docker system prune --all --volumes --force

  if [[ "${DELETE_KIND_NETWORK:-"false"}" == "true" ]]; then
    delete_kind_network
  fi
}

docker_cleanup 2>&1| prepend "docker_cleanup"

docker_create_kind_network
docker_run_local_registry "kind-registry" "5000"

cluster_prefix=""

CLUSTER_1="e2e-cluster-1"
CLUSTER_2="e2e-cluster-2"
CLUSTER_3="e2e-cluster-3"
CLUSTER_CENTRAL="e2e-operator"
CLUSTER_SINGLE="kind"

CTX_CLUSTER_1="kind-${CLUSTER_1}"
CTX_CLUSTER_2="kind-${CLUSTER_2}"
CTX_CLUSTER_3="kind-${CLUSTER_3}"
CTX_CLUSTER_CENTRAL="kind-${CLUSTER_CENTRAL}"
CTX_CLUSTER_SINGLE="kind-${CLUSTER_SINGLE}"


# To future maintainers: whenever modifying this bit, make sure you also update coredns.yaml
(scripts/dev/setup_kind_cluster.sh -n "${CLUSTER_CENTRAL}" -p "10.244.0.0/16" -s "10.96.0.0/16" -l "172.18.255.200-172.18.255.210" -c "${CLUSTER_DOMAIN}" 2>&1 | prepend "e2e-operator") &
(scripts/dev/setup_kind_cluster.sh -n "${CLUSTER_1}" -p "10.245.0.0/16" -s "10.97.0.0/16" -l "172.18.255.210-172.18.255.220" -c "${CLUSTER_DOMAIN}" 2>&1 | prepend "e2e-cluster-1") &
(scripts/dev/setup_kind_cluster.sh -n "${CLUSTER_2}" -p "10.246.0.0/16" -s "10.98.0.0/16" -l "172.18.255.220-172.18.255.230" -c "${CLUSTER_DOMAIN}" 2>&1 | prepend "e2e-cluster-2") &
(scripts/dev/setup_kind_cluster.sh -n "${CLUSTER_3}" -p "10.247.0.0/16" -s "10.99.0.0/16" -l "172.18.255.230-172.18.255.240" -c "${CLUSTER_DOMAIN}" 2>&1 | prepend "e2e-cluster-3") &
(scripts/dev/setup_kind_cluster.sh -n "${CLUSTER_SINGLE}" -l "172.18.255.200-172.18.255.250" -c "${CLUSTER_DOMAIN}" 2>&1 | prepend "kind") &

echo "Waiting for all kind clusters to be created"
wait

# we do exports sequentially as setup_kind_cluster.sh is run in parallel and we hit kube config locks
kind export kubeconfig --name "${CLUSTER_CENTRAL}"
kind export kubeconfig --name "${CLUSTER_1}"
kind export kubeconfig --name "${CLUSTER_2}"
kind export kubeconfig --name "${CLUSTER_3}"
kind export kubeconfig --name "${CLUSTER_SINGLE}"

echo "Interconnecting Kind clusters"
scripts/dev/interconnect_kind_clusters.sh -v "${CLUSTER_1}" "${CLUSTER_2}" "${CLUSTER_3}" "${CLUSTER_CENTRAL}" 2>&1 | prepend "interconnect_kind_clusters"

export VERSION=${VERSION:-1.16.1}

source multi_cluster/tools/download_istio.sh 2>&1 | prepend "download_istio" || true

VERSION=1.16.1 CTX_CLUSTER1="${CTX_CLUSTER_1}" CTX_CLUSTER2="${CTX_CLUSTER_2}" CTX_CLUSTER3="${CTX_CLUSTER_3}" multi_cluster/tools/install_istio.sh 2>&1 | prepend "install_istio" &
VERSION=1.16.1 CTX_CLUSTER="${CTX_CLUSTER_CENTRAL}" multi_cluster/tools/install_istio_central.sh 2>&1 | prepend "install_istio_central" &

wait

source scripts/dev/install_csi_driver.sh
csi_driver_download 2>&1 | prepend "csi_driver_download"

csi_driver_deploy ${CTX_CLUSTER_CENTRAL} 2>&1 | prepend "install_csi_driver.sh ${CTX_CLUSTER_CENTRAL}" &
csi_driver_deploy ${CTX_CLUSTER_1} 2>&1 | prepend "install_csi_driver.sh ${CTX_CLUSTER_1}" &
csi_driver_deploy ${CTX_CLUSTER_2} 2>&1 | prepend "install_csi_driver.sh ${CTX_CLUSTER_2}" &
csi_driver_deploy ${CTX_CLUSTER_3} 2>&1 | prepend "install_csi_driver.sh ${CTX_CLUSTER_3}" &
csi_driver_deploy ${CTX_CLUSTER_SINGLE} 2>&1 | prepend "install_csi_driver.sh ${CTX_CLUSTER_SINGLE}" &

wait
