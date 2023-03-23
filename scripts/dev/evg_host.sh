#!/usr/bin/env bash

# This is a helper script for running tests on Evergreen Hosts.
# It allows to configure kind clusters and expose remote API servers on a local machine to
# enable local development while running Kind cluster on EC2 instance.
# Run "evg_host.sh help" command to see the full usage.
# See docs/dev/local_e2e_testing.md for a tutorial how to use it.

set -Eeou pipefail

source scripts/dev/set_env_context.sh

if [[ -z "${EVG_HOST_NAME}" ]]; then
  echo "EVG_HOST_NAME env var is missing"
  exit 1
fi

get_host_url() {
  host=$(evergreen host list --json | jq -r ".[] | select (.name==\"${EVG_HOST_NAME}\") | .host_name ")
  if [[ "${host}" == "" ]]; then
    >&2 echo "Cannot find running EVG host with name ${EVG_HOST_NAME}.
Run evergreen host list --json or visit https://spruce.mongodb.com/spawn/host."
    exit 1
  fi
  echo "ubuntu@${host}"
}

cmd=${1-""}

if [[ "${cmd}" != "" && "${cmd}" != "help" ]]; then
  host_url=$(get_host_url)
fi

kubeconfig_path="$HOME/.operator-dev/evg-host.kubeconfig"

configure() {
  echo "Configuring ${host_url}..."
  ssh -T -q "${host_url}" "mkdir -p ~/multi_cluster/tools; mkdir -p ~/scripts/dev"
  scp -r multi_cluster/tools "${host_url}:~/multi_cluster/"
  scp -r scripts/dev "${host_url}:~/scripts/"

  ssh -T -q "${host_url}" <<"EOF"
cd ~

chmod +x ~/scripts/dev/*.sh
chmod +x ~/multi_cluster/tools/*.sh

echo "Increasing fs.inotify.max_user_instances"
sudo sysctl -w fs.inotify.max_user_instances=8192

download_kind() {
  echo "Downloading kind..."
  curl -s -o ./kind -L https://kind.sigs.k8s.io/dl/v0.17.0/kind-linux-amd64
  chmod +x ./kind
  sudo mv ./kind /usr/local/bin/kind
}

download_curl() {
  echo "Downloading curl..."
  curl -s -o kubectl -L https://dl.k8s.io/release/"$(curl -L -s https://dl.k8s.io/release/stable.txt)"/bin/linux/amd64/kubectl
  chmod +x kubectl
  sudo mv kubectl /usr/local/bin/kubectl
}

download_helm() {
  echo "Downloading helm..."
  curl -s -o helm.tar.gz -L https://get.helm.sh/helm-v3.10.3-linux-amd64.tar.gz
  tar -xf helm.tar.gz &2>/dev/null
  sudo mv linux-amd64/helm /usr/local/bin/helm
  rm helm.tar.gz
  rm -rf linux-amd64/
}
download_kind &
download_curl &
download_helm &
wait

EOF
}

get-kubeconfig() {
    scp "${host_url}:/home/ubuntu/.kube/config" "${kubeconfig_path}"
}

recreate-kind-clusters() {
  echo "Recreating kind clusters on ${EVG_HOST_NAME} (${host_url})..."
  # shellcheck disable=SC2088
  ssh -T "${host_url}" "~/scripts/dev/recreate_kind_clusters.sh"
  echo "Copying kubeconfig to ${kubeconfig_path}"
  get-kubeconfig
}

recreate-kind-cluster() {
  shift 1
  cluster_name=$1
  echo "Recreating kind cluster ${cluster_name} on ${EVG_HOST_NAME} (${host_url})..."
  # shellcheck disable=SC2088
  ssh -T "${host_url}" "~/scripts/dev/recreate_kind_cluster.sh ${cluster_name}"
  echo "Copying kubeconfig to ${kubeconfig_path}"
  get-kubeconfig
}


tunnel() {
  api_servers=$(yq '.clusters.[].cluster.server' < "${kubeconfig_path}" | sed 's/https:\/\///g')
  port_forwards=()
  for api_server in ${api_servers}; do
    host=$(echo "${api_server}" | cut -d ':' -f1)
    port=$(echo "${api_server}" | cut -d ':' -f2)
    if [[ "${port}" == "${host}" ]]; then
      port="443"
    fi
    port_forwards+=("-L" "${port}:${host}:${port}")
  done

  set -x
  # shellcheck disable=SC2029
  ssh "${port_forwards[@]}" "${host_url}"
  set +x
}

ssh_to_host() {
  shift 1
  # shellcheck disable=SC2029
  ssh "$@" "${host_url}"
}

usage() {
  echo "USAGE:
  evg_host.sh <command>

PREREQUISITES:
  - create EVG host: https://spruce.mongodb.com/spawn/host
  - install evergreen cli and set api-key in ~/.evergreen.yml: https://spruce.mongodb.com/preferences/cli
  - define in context EVG_HOST_NAME
  - VPN connection

COMMANDS:
  configure                 installs on a host: required software, copies scripts
  recreate-kind-clusters    executes scripts/dev/recreate_kind_clusters.sh and executes get-kubeconfig
  get-kubeconfig            copies remote kubeconfig locally to ~/.operator-dev/evg-host.kubeconfig
  tunnel                    creates ssh session with tunneling to all API servers
  ssh [args]                creates ssh session passing optional arguments to ssh
  help                      this message
"
}

case $cmd in
configure) configure ;;
recreate-kind-clusters) recreate-kind-clusters ;;
recreate-kind-cluster) recreate-kind-cluster "$@" ;;
get-kubeconfig) get-kubeconfig ;;
ssh) ssh_to_host "$@" ;;
tunnel) tunnel ;;
help) usage ;;
*) usage ;;
esac
