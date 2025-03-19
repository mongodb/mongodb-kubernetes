#!/usr/bin/env bash
set -Eeou pipefail

source scripts/dev/set_env_context.sh
source scripts/funcs/kubernetes

kind delete clusters --all

# first script prepares registry, so to avoid race it have to finish running before we execute subsequent ones in parallel
# To future maintainers: whenever modifying this bit, make sure you also update coredns.yaml
scripts/dev/setup_kind_cluster.sh -n "e2e-operator" -p "10.244.0.0/16" -s "10.96.0.0/16" -l "172.18.255.200-172.18.255.210" -c "${CLUSTER_DOMAIN}"
scripts/dev/setup_kind_cluster.sh -n "e2e-cluster-1" -p "10.245.0.0/16" -s "10.97.0.0/16" -l "172.18.255.210-172.18.255.220" -c "${CLUSTER_DOMAIN}" &
scripts/dev/setup_kind_cluster.sh -n "e2e-cluster-2" -p "10.246.0.0/16" -s "10.98.0.0/16" -l "172.18.255.220-172.18.255.230" -c "${CLUSTER_DOMAIN}" &
scripts/dev/setup_kind_cluster.sh -n "e2e-cluster-3" -p "10.247.0.0/16" -s "10.99.0.0/16" -l "172.18.255.230-172.18.255.240" -c "${CLUSTER_DOMAIN}" &

echo "Waiting for recreate_kind_cluster.sh to complete"
wait

# we do exports sequentially as setup_kind_cluster.sh is run in parallel and we hit kube config locks
kind export kubeconfig --name "e2e-operator"
kind export kubeconfig --name "e2e-cluster-1"
kind export kubeconfig --name "e2e-cluster-2"
kind export kubeconfig --name "e2e-cluster-3"

echo "Interconnecting Kind clusters"
scripts/dev/interconnect_kind_clusters.sh -v e2e-cluster-1 e2e-cluster-2 e2e-cluster-3 e2e-operator

export VERSION=${VERSION:-1.16.1}

source multi_cluster/tools/download_istio.sh

VERSION=1.16.1 CTX_CLUSTER1=kind-e2e-cluster-1 CTX_CLUSTER2=kind-e2e-cluster-2 CTX_CLUSTER3=kind-e2e-cluster-3 multi_cluster/tools/install_istio.sh &
VERSION=1.16.1 CTX_CLUSTER=kind-e2e-operator multi_cluster/tools/install_istio_central.sh &

CTX_CLUSTER=kind-e2e-operator CTX_CLUSTER1=kind-e2e-cluster-1 CTX_CLUSTER2=kind-e2e-cluster-2 CTX_CLUSTER3=kind-e2e-cluster-3 scripts/dev/install_csi_driver.sh &
wait
