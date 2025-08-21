#!/usr/bin/env bash

# This is a helper script for running tests on IBM Hosts using RHEL and Minikube.

set -Eeou pipefail

test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

source scripts/dev/set_env_context.sh
source scripts/funcs/printing
source scripts/funcs/install

if [[ -z "${MINIKUBE_HOST_NAME}" ]]; then
  echo "MINIKUBE_HOST_NAME env var is missing"
  echo "Set it to your IBM RHEL Host host connection string (e.g., user@hostname)"
  exit 1
fi

get_host_url() {
  echo "${MINIKUBE_HOST_NAME}"
}

cmd=${1-""}

if [[ "${cmd}" != "" && "${cmd}" != "help" ]]; then
  host_url=$(get_host_url)
fi

kubeconfig_path="${HOME}/.operator-dev/minikube-host.kubeconfig"

configure() {
  ssh -T -q "${host_url}" "sudo chown \$(whoami):\$(whoami) ~/.docker || true; mkdir -p ~/.docker"
  if [[ -f "${HOME}/.docker/config.json" ]]; then
    echo "Copying local ~/.docker/config.json authorization credentials to IBM RHEL Host host"
    jq '. | with_entries(select(.key == "auths"))' "${HOME}/.docker/config.json" | ssh -T -q "${host_url}" 'cat > ~/.docker/config.json'
  fi

  sync

  ssh -T -q "${host_url}" "cd ~/mongodb-kubernetes; scripts/dev/switch_context.sh root-context; scripts/minikube/setup_minikube_host.sh "
}

sync() {
  rsync --verbose --archive --compress --human-readable --recursive --progress \
  --delete --delete-excluded --max-size=1000000 --prune-empty-dirs \
  -e ssh \
  --include-from=.rsyncinclude \
  --exclude-from=.gitignore \
  --exclude-from=.rsyncignore \
  ./ "${host_url}:~/mongodb-kubernetes/"

  rsync --verbose --no-links --recursive --prune-empty-dirs --archive --compress --human-readable \
    --max-size=1000000 \
    -e ssh \
    ~/.operator-dev/ \
    "${host_url}:~/.operator-dev" &

  wait
}

remote-prepare-local-e2e-run() {
  set -x
  sync
  cmd make switch context=e2e_mdb_kind_ubi_cloudqa
  cmd scripts/dev/prepare_local_e2e_run.sh
  rsync --verbose --no-links --recursive --prune-empty-dirs --archive --compress --human-readable \
    --max-size=1000000 \
    -e ssh \
    "${host_url}:~/mongodb-kubernetes/.multi_cluster_local_test_files" \
    ./ &
  scp "${host_url}:~/.operator-dev/multicluster_kubeconfig" "${KUBE_CONFIG_PATH}" &

  wait
}

get-kubeconfig() {
  # For minikube, we need to get the kubeconfig and certificates
  echo "Getting kubeconfig from minikube on IBM RHEL Host host..."

  # Create local minikube directory structure
  mkdir -p "${HOME}/.minikube"

  # Copy certificates from remote host
  echo "Copying minikube certificates..."
  scp "${host_url}:~/.minikube/ca.crt" "${HOME}/.minikube/"
  scp "${host_url}:~/.minikube/client.crt" "${HOME}/.minikube/"
  scp "${host_url}:~/.minikube/client.key" "${HOME}/.minikube/"

  # Get kubeconfig and update paths to local ones
  ssh -T -q "${host_url}" "cd ~/mongodb-kubernetes; export KUBE_ENVIRONMENT_NAME=minikube; kubectl config view --raw" > "${kubeconfig_path}"

  # Update certificate paths to local paths
  sed -i '' "s|/home/cloud-user/.minikube|${HOME}/.minikube|g" "${kubeconfig_path}"

  # Update server addresses to use localhost for tunneling
  sed -i '' "s|https://192.168.[0-9]*.[0-9]*:\([0-9]*\)|https://127.0.0.1:\1|g" "${kubeconfig_path}"

  echo "Copied minikube kubeconfig and certificates to ${kubeconfig_path}"
}

tunnel() {
  shift 1
  echo "Setting up tunnel for minikube cluster..."

  # Get the minikube API server port from remote host
  local api_port
  api_port=$(ssh -T -q "${host_url}" "cd ~/mongodb-kubernetes; minikube ip 2>/dev/null && echo ':8443' | tr -d '\n'")

  if [[ -z "${api_port}" ]]; then
    echo "Could not determine minikube API server details. Is the cluster running?"
    return 1
  fi

  # Extract just the port (8443)
  local port="8443"
  echo "Forwarding localhost:${port} to minikube cluster API server"

  # Forward the API server port through minikube
  set -x
  # shellcheck disable=SC2029
  ssh -L "${port}:$(ssh -T -q "${host_url}" "minikube ip"):${port}" "${host_url}" "$@"
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
    scp "${HOME}/.ssh/id_rsa" "${host_url}:~/.ssh/id_rsa"
    scp "${HOME}/.ssh/id_rsa.pub" "${host_url}:~/.ssh/id_rsa.pub"
    ssh -T -q "${host_url}" "chmod 700 ~/.ssh && chown -R \$(whoami):\$(whoami) ~/.ssh"
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
  minikube_host.sh <command>

PREREQUISITES:
  - IBM RHEL Host host with SSH access
  - define MINIKUBE_HOST_NAME env var (e.g., export MINIKUBE_HOST_NAME=user@hostname)
  - SSH key-based authentication configured

COMMANDS:
  configure <architecture>           installs on a host: calls sync, switches context, installs necessary software (auto-detects arch)
  sync                              rsync of project directory
  remote-prepare-local-e2e-run       executes prepare-local-e2e on the remote host
  get-kubeconfig                     copies remote minikube kubeconfig locally to ~/.operator-dev/minikube-host.kubeconfig
  tunnel [args]                      creates ssh session with tunneling to all API servers
  ssh [args]                         creates ssh session passing optional arguments to ssh
  cmd [command with args]            execute command as if being on IBM RHEL Host host
  upload-my-ssh-private-key          uploads your ssh keys (~/.ssh/id_rsa) to IBM RHEL Host host
  help                               this message

EXAMPLES:
  export MINIKUBE_HOST_NAME=user@ibmz8
  minikube_host.sh tunnel
  minikube_host.sh cmd 'make e2e test=replica_set'
"
}

case ${cmd} in
configure) configure "$@" ;;
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
