#!/usr/bin/env bash

# This is a helper script for running tests on Evergreen Hosts.
# It allows to configure kind clusters and expose remote API servers on a local machine to
# enable local development while running Kind cluster on EC2 instance.
# Run "evg_host.sh help" command to see the full usage.
# See docs/dev/local_e2e_testing.md for a tutorial how to use it.

set -Eeou pipefail

test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

source scripts/dev/set_env_context.sh
source scripts/funcs/printing


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

kubeconfig_path="${HOME}/.operator-dev/evg-host.kubeconfig"

configure() {
  shift || true
  auto_recreate="false"

  # Parse arguments
  while [[ $# -gt 0 ]]; do
    case $1 in
      --auto-recreate)
        auto_recreate="true"
        shift
        ;;
      *)
        echo "Unknown argument: $1"
        echo "Usage: configure [--auto-recreate]"
        exit 1
        ;;
    esac
  done

  echo "Configuring EVG host ${EVG_HOST_NAME} (${host_url}) (auto_recreate: ${auto_recreate})"

  ssh -T -q "${host_url}" "sudo chown ubuntu:ubuntu ~/.docker || true; mkdir -p ~/.docker"
  if [[ -f "${HOME}/.docker/config.json" ]]; then
    echo "Copying local ~/.docker/config.json authorization credentials to EVG host"
    jq '. | with_entries(select(.key == "auths"))' "${HOME}/.docker/config.json" | ssh -T -q "${host_url}" 'cat > /home/ubuntu/.docker/config.json'
  fi

  sync | prepend "sync"

  ssh -T -q "${host_url}" "cd ~/mongodb-kubernetes; scripts/dev/switch_context.sh root-context; scripts/dev/setup_evg_host.sh ${auto_recreate}"
}

sync() {
  rsync --verbose --archive --compress --human-readable --recursive --progress \
  --delete --delete-excluded --max-size=1000000 --prune-empty-dirs \
  -e ssh \
  --include-from=.rsyncinclude \
  --exclude-from=.gitignore \
  --exclude-from=.rsyncignore \
  ./ "${host_url}:/home/ubuntu/mongodb-kubernetes/"

  rsync --verbose --no-links --recursive --prune-empty-dirs --archive --compress --human-readable \
    --max-size=1000000 \
    -e ssh \
    ~/.operator-dev/ \
    "${host_url}:/home/ubuntu/.operator-dev" &

  wait
}

remote-prepare-local-e2e-run() {
  set -x
  sync
  cmd make switch context=appdb-multi
  cmd scripts/dev/prepare_local_e2e_run.sh
  rsync --verbose --no-links --recursive --prune-empty-dirs --archive --compress --human-readable \
    --max-size=1000000 \
    -e ssh \
    "${host_url}:/home/ubuntu/mongodb-kubernetes/.multi_cluster_local_test_files" \
    ./ &
  scp "${host_url}:/home/ubuntu/.operator-dev/multicluster_kubeconfig" "${KUBE_CONFIG_PATH}" &

  wait
}

get-kubeconfig() {
  remote_path="${host_url}:/home/ubuntu/.operator-dev/evg-host.kubeconfig"
  echo "Copying remote kubeconfig from ${remote_path} to ${kubeconfig_path}"
  scp "${remote_path}" "${kubeconfig_path}"
}

recreate-kind-clusters() {
  DELETE_KIND_NETWORK=${DELETE_KIND_NETWORK:-"false"}
  configure 2>&1| prepend "evg_host.sh configure"
  echo "Recreating kind clusters on ${EVG_HOST_NAME} (${host_url})..."
  # shellcheck disable=SC2088
  ssh -T "${host_url}" "cd ~/mongodb-kubernetes; DELETE_KIND_NETWORK=${DELETE_KIND_NETWORK} scripts/dev/recreate_kind_clusters.sh"
  echo "Copying kubeconfig to ${kubeconfig_path}"
  get-kubeconfig 2>&1| prepend "evg_host.sh configure"
}

recreate-kind-cluster() {
  shift 1
  cluster_name=$1
  configure 2>&1| prepend "evg_host.sh configure"
  echo "Recreating kind cluster ${cluster_name} on ${EVG_HOST_NAME} (${host_url})..."
  # shellcheck disable=SC2088
  ssh -T "${host_url}" "cd ~/mongodb-kubernetes; scripts/dev/recreate_kind_cluster.sh ${cluster_name}"
  echo "Copying kubeconfig to ${kubeconfig_path}"
  get-kubeconfig
}

tunnel() {
  shift 1
  get-kubeconfig
  # shellcheck disable=SC2016
  api_servers=$(yq '.contexts[].context.cluster as $cluster | .clusters[] | select(.name == $cluster).cluster.server' < "${kubeconfig_path}" | sed 's/https:\/\///g')
  echo "Extracted the following API server urls from ${kubeconfig_path}: ${api_servers}"
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
  ssh "${port_forwards[@]}" "${host_url}" "$@"
  set +x
}

retry_with_sleep() {
  shift 1
  cmd=$1
  local sleep_time
  sleep_time=5

  while true; do
    ${cmd} || true
    echo "Retrying command after ${sleep_time} of sleep: ${cmd}"
    sleep 5;
  done
}

ssh_to_host() {
  shift 1
  # shellcheck disable=SC2029
  ssh "$@" "${host_url}"
}

upload-my-ssh-private-key() {
    ssh -T -q "${host_url}" "mkdir -p ~/.ssh"
    scp "${HOME}/.ssh/id_rsa" "${host_url}:/home/ubuntu/.ssh/id_rsa"
    scp "${HOME}/.ssh/id_rsa.pub" "${host_url}:/home/ubuntu/.ssh/id_rsa.pub"
    ssh -T -q "${host_url}" "chmod 700 ~/.ssh && chown -R ubuntu:ubuntu ~/.ssh"
}

cmd() {
  if [[ "$1" == "cmd" ]]; then
    shift 1
  fi

  cmd="cd ~/mongodb-kubernetes; $*"
  ssh -T -q "${host_url}" "${cmd}"
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
  recreate-kind-clusters                all-you-need to configure host and kind clusters; deletes and recreates all kind clusters (for single and multi runs)
  configure [--auto-recreate]           installs on a host: calls sync, switches context, installs necessary software
  sync                                  rsync of project directory
  recreate-kind-cluster test-cluster    executes scripts/dev/recreate_kind_cluster.sh test-cluster and executes get-kubeconfig
  remote-prepare-local-e2e-run          executes prepare-local-e2e on the remote evg host
  get-kubeconfig                        copies remote kubeconfig locally to ~/.operator-dev/evg-host.kubeconfig
  tunnel [args]                         creates ssh session with tunneling to all API servers
  ssh [args]                            creates ssh session passing optional arguments to ssh
  cmd [command with args]               execute command as if being on evg host
  upload-my-ssh-private-key             uploads your ssh keys (~/.ssh/id_rsa) to evergreen host
  help                                  this message
"
}

case ${cmd} in
configure) configure "$@" ;;
recreate-kind-clusters) recreate-kind-clusters "$@" ;;
recreate-kind-cluster) recreate-kind-cluster "$@" ;;
get-kubeconfig) get-kubeconfig ;;
remote-prepare-local-e2e-run) remote-prepare-local-e2e-run ;;
ssh) ssh_to_host "$@" ;;
tunnel) retry_with_sleep tunnel "$@" ;;
sync) sync ;;
cmd) cmd "$@" ;;
upload-my-ssh-private-key) upload-my-ssh-private-key ;;
help) usage ;;
*) usage ;;
esac
