#!/usr/bin/env bash
set -Eeou pipefail -o posix

source scripts/dev/set_env_context.sh

####
## This script is based on https://gist.github.com/aojea/00bca6390f5f67c0a30db6acacf3ea91#multiple-clusters
####

function usage() {
  echo "Interconnects Pod and Service networks between multiple Kind clusters.

Usage:
  interconnect_kind_clusters.sh
  interconnect_kind_clusters.sh [-hv]

Options:
  -h                   (optional) Shows this screen.
  -v                   (optional) Verbose mode.
"
  exit 0
}

verbose=0
while getopts ':h:v' opt; do
    case ${opt} in
      (v)   verbose=1;;
      (h)   usage;;
      (*)   usage;;
    esac
done
shift "$((OPTIND-1))"

clusters=("$@")
echo "Interconnecting ${clusters[*]}"

routes=()
kind_nodes_for_exec=()
for c in "${clusters[@]}"; do
  # We need to explicitly ensure we're in a steady state. Kind often reports done while the API Server hasn't assigned Pod CIDRs yet
  kubectl --context "kind-${c}" wait nodes --all --for=condition=ready > /dev/null

  pod_route=$(kubectl --context "kind-${c}" get nodes -o=jsonpath='{range .items[*]}{"ip route add "}{.spec.podCIDR}{" via "}{.status.addresses[?(@.type=="InternalIP")].address}{"\n"}{end}')
  # Is there a better way to do it?
  service_cidr=$(kubectl --context "kind-${c}" --namespace kube-system get configmap kubeadm-config -o=jsonpath='{.data.ClusterConfiguration}' | grep "serviceSubnet" | cut -d\  -f4)
  service_route=$(kubectl --context "kind-${c}" get nodes -o=jsonpath='{range .items[*]}{"ip route add "}'"${service_cidr}"'{" via "}{.status.addresses[?(@.type=="InternalIP")].address}{"\n"}{end}')
  # shellcheck disable=SC2086
  kind_node_in_docker=$(kind get nodes --name ${c})

  if [ "${verbose}" -ne "0" ]; then
    echo "[${c}] [${kind_node_in_docker}] Pod Route: ${pod_route}"
    echo "[${c}] [${kind_node_in_docker}] Service Route: ${service_route}"
  fi


  routes+=("${pod_route}")
  routes+=("${service_route}")
  kind_nodes_for_exec+=("${kind_node_in_docker}")
done

if [ "${verbose}" -ne "0" ]; then
  echo "Injecting routes into the following Docker containers: ${clusters[*]}"
  echo "Gathered the following Routes to inject:"
  IFS=$'\n' eval 'echo "${routes[*]}"'
fi

for c in "${kind_nodes_for_exec[@]}"; do
  for r in "${routes[@]}"; do
    error_code=0
    # shellcheck disable=SC2086
    docker exec ${c} ${r} || error_code=$?
    if [ "${error_code}" -ne "0" ] && [ "${error_code}" -ne "2" ]; then
      echo "Error while interconnecting Kind clusters. Try debugging it manually by calling:"
      echo "docker exec ${c} ${r}"
      exit 1
    fi
  done
done
