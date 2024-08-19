#!/usr/bin/env bash
set -Eeou pipefail

source scripts/dev/set_env_context.sh

####
# This file is copy-pasted from https://github.com/mongodb/mongodb-kubernetes-operator/blob/master/scripts/dev/setup_kind_cluster.sh
# Do not edit !!!
####

run_docker() {
  docker run -d --restart=always -p "127.0.0.1:${reg_port}:5000" --name "${reg_name}" registry:2
}

function usage() {
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
while getopts ':c:l:p:s:n:her' opt; do
  case $opt in
  n) cluster_name=$OPTARG ;;
  e) export_kubeconfig=1 ;;
  r) recreate=1 ;;
  p) pod_network=$OPTARG ;;
  s) service_network=$OPTARG ;;
  l) metallb_ip_range=$OPTARG ;;
  c) cluster_domain=$OPTARG ;;
  h) usage ;;
  *) usage ;;
  esac
done
shift "$((OPTIND - 1))"

kubeconfig_path="$HOME/.kube/${cluster_name}"

# create the kind network early unless it already exists.
# it would normally be created automatically by kind but we
# need it earlier to get the IP address of our registry.
docker network create kind || true

# adapted from https://kind.sigs.k8s.io/docs/user/local-registry/
# create registry container unless it already exists
reg_name='kind-registry'
reg_port='5000'
running="$(docker inspect -f '{{.State.Running}}' "${reg_name}" 2>/dev/null || true)"

max_retries=3
retry_count=0

success=false

if [ "${running}" != 'true' ]; then
  while [ "$retry_count" -lt "$max_retries" ]; do
    if run_docker; then
      echo "Docker container started successfully."
      success=true
      break
    else
      echo "Docker run failed. Attempting to restart Docker service and retrying"
    fi
  done

  if [ "$success" = false ]; then
    echo "Docker run command failed after $max_retries attempts!"
    exit 1
  fi
fi

if [ "${recreate}" != 0 ]; then
  kind delete cluster --name "${cluster_name}" || true
fi

# create a cluster with the local registry enabled in containerd
if [ "$KUBE_ENVIRONMENT_NAME" = "performance" ]; then
  echo "installing kind with more nodes with performance"
  cat <<EOF | kind create cluster --name "${cluster_name}" --kubeconfig "${kubeconfig_path}" --wait 700s --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  image: kindest/node:v1.30.4@sha256:976ea815844d5fa93be213437e3ff5754cd599b040946b5cca43ca45c2047114
  extraMounts:
  - containerPath: /var/lib/kubelet/config.json
    hostPath: ${HOME}/.docker/config.json
- role: control-plane
  image: kindest/node:v1.30.4@sha256:976ea815844d5fa93be213437e3ff5754cd599b040946b5cca43ca45c2047114
  extraMounts:
  - containerPath: /var/lib/kubelet/config.json
    hostPath: ${HOME}/.docker/config.json
- role: control-plane
  image: kindest/node:v1.30.4@sha256:976ea815844d5fa93be213437e3ff5754cd599b040946b5cca43ca45c2047114
  extraMounts:
  - containerPath: /var/lib/kubelet/config.json
    hostPath: ${HOME}/.docker/config.json
- role: worker
  image: kindest/node:v1.30.4@sha256:976ea815844d5fa93be213437e3ff5754cd599b040946b5cca43ca45c2047114
  extraMounts:
  - containerPath: /var/lib/kubelet/config.json
    hostPath: ${HOME}/.docker/config.json
- role: worker
  image: kindest/node:v1.30.4@sha256:976ea815844d5fa93be213437e3ff5754cd599b040946b5cca43ca45c2047114
  extraMounts:
  - containerPath: /var/lib/kubelet/config.json
    hostPath: ${HOME}/.docker/config.json
- role: worker
  image: kindest/node:v1.30.4@sha256:976ea815844d5fa93be213437e3ff5754cd599b040946b5cca43ca45c2047114
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
else
  cat <<EOF | kind create cluster --name "${cluster_name}" --kubeconfig "${kubeconfig_path}" --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  image: kindest/node:v1.30.4@sha256:976ea815844d5fa93be213437e3ff5754cd599b040946b5cca43ca45c2047114
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
fi

echo "finished installing kind"

# connect the registry to the cluster network if not already connected
if [ "$(docker inspect -f='{{json .NetworkSettings.Networks.kind}}' "${reg_name}")" = 'null' ]; then
  docker network connect "kind" "${reg_name}"
fi

echo "installing registry"
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

echo "testing for nodes to be ready"
# Install MetalLB, before we start, we need to ensure the Kind Nodes are up
kubectl --kubeconfig "${kubeconfig_path}" wait nodes --all --for=condition=ready --timeout=600s >/dev/null

echo "installing metallb"
kubectl get --kubeconfig "${kubeconfig_path}" nodes -owide
kubectl apply --kubeconfig "${kubeconfig_path}" --timeout=600s -f https://raw.githubusercontent.com/metallb/metallb/v0.13.7/config/manifests/metallb-native.yaml

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

if [[ "${export_kubeconfig}" == "1" ]]; then
  kind export kubeconfig --name "${cluster_name}"
fi
