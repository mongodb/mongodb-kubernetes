#!/usr/bin/env bash

# This is a helper script for running tests on s390x Hosts.
# It allows to configure minikube clusters and expose remote API servers on a local machine to
# enable local development while running minikube cluster on s390x instance.
# Run "minikube_host.sh help" command to see the full usage.
# Similar to evg_host.sh but uses minikube instead of kind.

set -Eeou pipefail

test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

source scripts/dev/set_env_context.sh
source scripts/funcs/printing

if [[ -z "${S390_HOST_NAME}" ]]; then
  echo "S390_HOST_NAME env var is missing"
  echo "Set it to your s390x host connection string (e.g., user@hostname)"
  exit 1
fi

get_host_url() {
  echo "${S390_HOST_NAME}"
}

cmd=${1-""}

if [[ "${cmd}" != "" && "${cmd}" != "help" ]]; then
  host_url=$(get_host_url)
fi

kubeconfig_path="${HOME}/.operator-dev/s390-host.kubeconfig"

configure() {
  shift 1
  arch=${1-"$(uname -m)"}

  echo "Configuring minikube host ${S390_HOST_NAME} (${host_url}) with architecture ${arch}"

  if [[ "${cmd}" == "configure" && ! "${arch}" =~ ^(s390x|ppc64le|x86_64|aarch64)$ ]]; then
    echo "'configure' command supports the following architectures: s390x, ppc64le, x86_64, aarch64"
    exit 1
  fi

  ssh -T -q "${host_url}" "sudo chown \$(whoami):\$(whoami) ~/.docker || true; mkdir -p ~/.docker"
  if [[ -f "${HOME}/.docker/config.json" ]]; then
    echo "Copying local ~/.docker/config.json authorization credentials to s390x host"
    jq '. | with_entries(select(.key == "auths"))' "${HOME}/.docker/config.json" | ssh -T -q "${host_url}" 'cat > ~/.docker/config.json'
  fi

  sync

  ssh -T -q "${host_url}" "cd ~/mongodb-kubernetes; scripts/dev/switch_context.sh root-context; scripts/minikube/setup_minikube_host.sh ${arch}"
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
  echo "Getting kubeconfig from minikube on s390x host..."
  local profile=${MINIKUBE_PROFILE:-mongodb-e2e}

  # Create local minikube directory structure
  mkdir -p "${HOME}/.minikube/profiles/${profile}"

  # Copy certificates from remote host
  echo "Copying minikube certificates..."
  scp "${host_url}:~/.minikube/ca.crt" "${HOME}/.minikube/"
  scp "${host_url}:~/.minikube/profiles/${profile}/client.crt" "${HOME}/.minikube/profiles/${profile}/"
  scp "${host_url}:~/.minikube/profiles/${profile}/client.key" "${HOME}/.minikube/profiles/${profile}/"

  # Get kubeconfig and update paths to local ones
  ssh -T -q "${host_url}" "cd ~/mongodb-kubernetes; export KUBE_ENVIRONMENT_NAME=minikube; export MINIKUBE_PROFILE=${profile}; kubectl config view --raw" > "${kubeconfig_path}"

  # Update certificate paths to local paths
  sed -i '' "s|/home/cloud-user/.minikube|${HOME}/.minikube|g" "${kubeconfig_path}"

  # Update server addresses to use localhost for tunneling
  sed -i '' "s|https://192.168.[0-9]*.[0-9]*:\([0-9]*\)|https://127.0.0.1:\1|g" "${kubeconfig_path}"

  echo "Copied minikube kubeconfig and certificates to ${kubeconfig_path}"
}

recreate-minikube-cluster() {
  shift 1
  profile_name=${1:-mongodb-e2e}
  configure "$(uname -m)" 2>&1| prepend "minikube_host.sh configure"
  echo "Recreating minikube cluster ${profile_name} on ${S390_HOST_NAME} (${host_url})..."
  # shellcheck disable=SC2088
  ssh -T "${host_url}" "cd ~/mongodb-kubernetes; export KUBE_ENVIRONMENT_NAME=minikube; export MINIKUBE_PROFILE=${profile_name}; minikube delete --profile=${profile_name} || true; minikube start --profile=${profile_name} --driver=docker --memory=8192mb --cpus=4"
  echo "Copying kubeconfig to ${kubeconfig_path}"
  get-kubeconfig
}

tunnel() {
  shift 1
  echo "Setting up tunnel for minikube cluster..."
  local profile=${MINIKUBE_PROFILE:-mongodb-e2e}

  # Get the minikube API server port from remote host
  local api_port
  api_port=$(ssh -T -q "${host_url}" "cd ~/mongodb-kubernetes; export MINIKUBE_PROFILE=${profile}; minikube ip --profile=${profile} 2>/dev/null && echo ':8443' | tr -d '\n'")

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
  ssh -L "${port}:$(ssh -T -q "${host_url}" "export MINIKUBE_PROFILE=${profile}; minikube ip --profile=${profile}"):${port}" "${host_url}" "$@"
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
  - s390x host with SSH access
  - define S390_HOST_NAME env var (e.g., export S390_HOST_NAME=user@hostname)
  - SSH key-based authentication configured

COMMANDS:
  configure <architecture>           installs on a host: calls sync, switches context, installs necessary software (auto-detects arch)
  sync                              rsync of project directory
  recreate-minikube-cluster <profile>  recreates minikube cluster with specific profile and executes get-kubeconfig
  remote-prepare-local-e2e-run       executes prepare-local-e2e on the remote host
  get-kubeconfig                     copies remote minikube kubeconfig locally to ~/.operator-dev/s390-host.kubeconfig
  tunnel [args]                      creates ssh session with tunneling to all API servers
  ssh [args]                         creates ssh session passing optional arguments to ssh
  cmd [command with args]            execute command as if being on s390x host
  upload-my-ssh-private-key          uploads your ssh keys (~/.ssh/id_rsa) to s390x host
  help                               this message

EXAMPLES:
  export S390_HOST_NAME=user@ibmz8
  minikube_host.sh tunnel
  minikube_host.sh cmd 'make e2e test=replica_set'
"
}

case ${cmd} in
configure) configure "$@" ;;
recreate-minikube-cluster) recreate-minikube-cluster "$@" ;;
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
