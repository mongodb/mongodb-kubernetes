#!/usr/bin/env bash
set -Eeou pipefail

test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

source scripts/dev/set_env_context.sh
source scripts/funcs/kubernetes

usage() {
  echo "Deploy local registry and create kind cluster configured to use this registry. Local Docker registry is deployed at localhost:5000.

Usage:
  setup_kind_cluster.sh [-n <cluster_name>] [-r]
  setup_kind_cluster.sh [-h]
  setup_kind_cluster.sh [-n <cluster_name>] [-e] [-r]

Options:
  -n <cluster_name>    (optional) Set kind cluster name to <cluster_name>. Creates kubeconfig in ~/.kube/<cluster_name>. The default name is 'kind' if not set.
  -e                   (optional) Export newly created kind cluster's credentials to ~/.kube/<cluster_name> and set current kubectl context.
  -h                   (optional) Shows this screen.
  -r                   (optional) Recreate cluster if needed
  -p <pod network>     (optional) Network reserved for Pods, e.g. 10.244.0.0/16
  -s <service network> (optional) Network reserved for Services, e.g. 10.96.0.0/16
  -l <LB IP range>     (optional) MetalLB IP range, e.g. 172.18.255.200-172.18.255.250
  -c <cluster domain>  (optional) Cluster domain. If not supplied, cluster.local will be used
  -g                   (optional) Run docker registry before installing kind
  -k                   (optional) Create docker network before installing kind
"
  exit 0
}

cluster_name=${CLUSTER_NAME:-"kind"}
cluster_domain="cluster.local"
export_kubeconfig=0
recreate=0
pod_network="10.244.0.0/16"
service_network="10.96.0.0/16"
metallb_ip_range="172.18.255.200-172.18.255.250"
install_registry=0
configure_docker_network=0
while getopts ':n:p:s:c:l:egrhk' opt; do
  case ${opt} in
  n) cluster_name=${OPTARG} ;;
  p) pod_network=${OPTARG} ;;
  s) service_network=${OPTARG} ;;
  c) cluster_domain=${OPTARG} ;;
  l) metallb_ip_range=${OPTARG} ;;
  e) export_kubeconfig=1 ;;
  g) install_registry=1 ;;
  r) recreate=1 ;;
  k) configure_docker_network=1 ;;
  h) usage ;;
  *) usage ;;
  esac
done
shift "$((OPTIND - 1))"

kubeconfig_path="${HOME}/.kube/${cluster_name}"

# create a cluster with the local registry enabled in containerd
registry="docker.io"
if [[ "${RUNNING_IN_EVG:-false}" == "true" ]]; then
  registry="268558157000.dkr.ecr.eu-west-1.amazonaws.com/docker-hub-mirrors"
fi

metallb_version="v0.13.7"
metrics_server_version="v0.7.2"

reg_name='kind-registry'
reg_port='5000'
kind_image="${registry}/kindest/node:v1.34.0@sha256:7416a61b42b1662ca6ca89f02028ac133a309a2a30ba309614e8ec94d976dc5a"

kind_delete_cluster() {
  kind delete cluster --name "${cluster_name}" || true
}

kind_create_cluster_for_performance_tests() {
  echo "installing kind with more nodes with performance"
  cat <<EOF | kind create cluster --name "${cluster_name}" --kubeconfig "${kubeconfig_path}" --wait 700s -v=5 --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  image: ${kind_image}
  extraMounts:
  - containerPath: /var/lib/kubelet/config.json
    hostPath: ${HOME}/.docker/config.json
- role: control-plane
  image: ${kind_image}
  extraMounts:
  - containerPath: /var/lib/kubelet/config.json
    hostPath: ${HOME}/.docker/config.json
- role: control-plane
  image: ${kind_image}
  extraMounts:
  - containerPath: /var/lib/kubelet/config.json
    hostPath: ${HOME}/.docker/config.json
- role: worker
  image: ${kind_image}
  extraMounts:
  - containerPath: /var/lib/kubelet/config.json
    hostPath: ${HOME}/.docker/config.json
- role: worker
  image: ${kind_image}
  extraMounts:
  - containerPath: /var/lib/kubelet/config.json
    hostPath: ${HOME}/.docker/config.json
- role: worker
  image: ${kind_image}
  extraMounts:
  - containerPath: /var/lib/kubelet/config.json
    hostPath: ${HOME}/.docker/config.json
networking:
  podSubnet: "${pod_network}"
  serviceSubnet: "${service_network}"
kubeadmConfigPatches:
- |
  apiVersion: kubeadm.k8s.io/v1beta3
  kind: ClusterConfiguration
  networking:
    dnsDomain: "${cluster_domain}"
containerdConfigPatches:
- |-
  [plugins."io.containerd.grpc.v1.cri".registry.mirrors."localhost:${reg_port}"]
    endpoint = ["http://${reg_name}:${reg_port}"]
EOF
}

kind_create_cluster() {
  cat <<EOF | kind create cluster --name "${cluster_name}" --kubeconfig "${kubeconfig_path}" --wait 700s -v 5 --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  image: ${kind_image}
  extraMounts:
  - containerPath: /var/lib/kubelet/config.json
    hostPath: ${HOME}/.docker/config.json
networking:
  podSubnet: "${pod_network}"
  serviceSubnet: "${service_network}"
kubeadmConfigPatches:
- |
  apiVersion: kubeadm.k8s.io/v1beta3
  kind: ClusterConfiguration
  networking:
    dnsDomain: "${cluster_domain}"
containerdConfigPatches:
- |-
  [plugins."io.containerd.grpc.v1.cri".registry.mirrors."localhost:${reg_port}"]
    endpoint = ["http://${reg_name}:${reg_port}"]
EOF
  echo "finished installing kind"
}

kind_configure_local_registry(){
  echo "configuring local registry"
  # Document the local registry (from  https://kind.sigs.k8s.io/docs/user/local-registry/)
  # https://github.com/kubernetes/enhancements/tree/master/keps/sig-cluster-lifecycle/generic/1755-communicating-a-local-registry
  cat <<EOF | kubectl apply --kubeconfig "${kubeconfig_path}" -f -
apiVersion: v1
kind: ConfigMap
metadata:
  name: local-registry-hosting
  namespace: kube-public
data:
  localRegistryHosting.v1: |
    host: "localhost:${reg_port}"
    help: "https://kind.sigs.k8s.io/docs/user/local-registry/"
EOF
}

function kind_wait_for_nodes_are_ready() {
  echo "testing for nodes to be ready"
  kubectl --kubeconfig "${kubeconfig_path}" wait nodes --all --for=condition=ready --timeout=600s >/dev/null
}

function kind_install_metallb() {
  echo "installing metallb"
  kubectl get --kubeconfig "${kubeconfig_path}" nodes -owide
  kubectl apply --kubeconfig "${kubeconfig_path}" --timeout=600s -f https://raw.githubusercontent.com/metallb/metallb/${metallb_version}/config/manifests/metallb-native.yaml

  echo "waiting metallb to be ready"
  kubectl wait --kubeconfig "${kubeconfig_path}" --timeout=3000s --namespace metallb-system \
    --for=condition=ready pod \
    --selector=app=metallb

  echo "install metallb to be ready"
  cat <<EOF | kubectl apply --validate='false' --kubeconfig "${kubeconfig_path}" -f -
apiVersion: metallb.io/v1beta1
kind: IPAddressPool
metadata:
  name: default-address-pool
  namespace: metallb-system
spec:
  addresses:
  - "${metallb_ip_range}"
---
apiVersion: metallb.io/v1beta1
kind: L2Advertisement
metadata:
  name: empty
  namespace: metallb-system
EOF
}

kind_install_metrics_server() {
  kubectl apply --kubeconfig "${kubeconfig_path}" -f "https://github.com/kubernetes-sigs/metrics-server/releases/download/${metrics_server_version}/components.yaml"
  kubectl patch --kubeconfig "${kubeconfig_path}" -n kube-system deployment metrics-server --type=json \
    -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'
  kubectl patch --kubeconfig "${kubeconfig_path}" -n kube-system deployment metrics-server --type=json -p \
    '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--metric-resolution=15s"}]'
}

export_kubeconfig() {
  kind export kubeconfig --name "${cluster_name}"
}

# main script

if [[ "${install_registry}" == "1" ]]; then
  docker_run_local_registry "$@"
fi

if [ "${recreate}" != 0 ]; then
  kind_delete_cluster
fi

if [ "${configure_docker_network}" != 0 ]; then
  docker_create_kind_network
  docker_network_connect_to_kind
fi
if [ "${KUBE_ENVIRONMENT_NAME}" = "performance" ]; then
  kind_create_cluster_for_performance_tests
else
  kind_create_cluster
fi

kind_configure_local_registry
kind_wait_for_nodes_are_ready
kind_install_metallb
kind_install_metrics_server

if [[ "${export_kubeconfig}" == "1" ]]; then
  export_kubeconfig
fi
