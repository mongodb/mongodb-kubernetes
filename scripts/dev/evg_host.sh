#!/usr/bin/env bash

# This is a helper script for running tests on Evergreen Hosts.
# It allows to configure kind clusters and expose remote API servers on a local machine to
# enable local development while running Kind cluster on EC2 instance.
# Run "evg_host.sh help" command to see the full usage.

set -Eeou pipefail

test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

source scripts/dev/set_env_context.sh
source scripts/funcs/printing


if [[ -z "${EVG_HOST_NAME}" ]]; then
  echo "EVG_HOST_NAME env var is missing"
  exit 1
fi

get_host_url() {
  if [[ -z "${EVG_HOST_ADDRESS:-}" ]]; then
    host=$(evergreen host list --json | jq -r ".[] | select (.name==\"${EVG_HOST_NAME}\") | .host_name ")
    if [[ "${host}" == "" ]]; then
      >&2 echo "Cannot find running EVG host with name ${EVG_HOST_NAME}.
  Run evergreen host list --json or visit https://spruce.mongodb.com/spawn/host."
      exit 1
    fi
  else
    host="${EVG_HOST_ADDRESS}"
  fi
  echo "ubuntu@${host}"
}

cmd=${1-""}

if [[ "${cmd}" != "" && "${cmd}" != "help" ]]; then
  host_url=$(get_host_url)
fi

kubeconfig_path="${PROJECT_DIR}/.generated/current.kubeconfig"

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

  ssh -T -q "${host_url}" "cd ~/mongodb-kubernetes; scripts/dev/switch_context.sh root-context; . scripts/dev/devenv; scripts/dev/setup_evg_host.sh ${auto_recreate}"
}

sync() {
  rsync --verbose --archive --compress --human-readable --recursive --progress \
  --delete --delete-excluded --max-size=1000000 --prune-empty-dirs \
  -e ssh \
  --include-from=.rsyncinclude \
  --exclude-from=.gitignore \
  --exclude-from=.rsyncignore \
  ./ "${host_url}:/home/ubuntu/mongodb-kubernetes/"

  # Push local ~/.operator-dev/ (om creds, contexts, etc.) to the EVG host.
  # multicluster_kubeconfig is excluded: it flows REMOTE -> LOCAL via
  # remote-prepare-local-e2e-run, and including it here could clobber the
  # populated remote file with a stale local copy.
  rsync --verbose --no-links --recursive --prune-empty-dirs --archive --compress --human-readable \
    --max-size=1000000 \
    -e ssh \
    --exclude='multicluster_kubeconfig' \
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
  # KUBE_CONFIG_PATH on the remote also resolves to
  # /home/ubuntu/mongodb-kubernetes/.generated/multicluster_kubeconfig
  # (root-context expands ${PROJECT_DIR}/.generated/multicluster_kubeconfig
  # against the remote checkout) — so the source path is fixed, the
  # destination follows the local worktree's KUBE_CONFIG_PATH.
  scp "${host_url}:/home/ubuntu/mongodb-kubernetes/.generated/multicluster_kubeconfig" "${KUBE_CONFIG_PATH}" &

  wait
}

get-kubeconfig() {
  # Pull the EVG host's checked-in kubeconfig down to the worktree, then
  # patch in the proxy-url and register with the kube-forwarding-proxy if
  # we're inside an environment that has them set (devcontainer).
  #
  # Usage: get-kubeconfig [--no-fetch]
  #   --no-fetch  Skip the scp step. Use when the kubeconfig already lives
  #               in this filesystem (e.g. inside the devcontainer where
  #               .generated/ is bind-mounted from the host) and we just
  #               need to apply the proxy-url + k8s-proxy registration.
  shift || true
  local no_fetch=0
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --no-fetch) no_fetch=1; shift ;;
      *) echo "Unknown argument to get-kubeconfig: $1" >&2; return 1 ;;
    esac
  done

  if [[ ${no_fetch} -eq 0 ]]; then
    # The remote `kind export kubeconfig` writes to ${KUBECONFIG}, which on
    # the EVG host site-context resolves to
    # /home/ubuntu/mongodb-kubernetes/.generated/current.kubeconfig
    # (the canonical per-side kubeconfig path). Scp from that exact path.
    remote_path="${host_url}:/home/ubuntu/mongodb-kubernetes/.generated/current.kubeconfig"
    echo "Copying remote kubeconfig from ${remote_path} to ${kubeconfig_path}"
    mkdir -p "$(dirname "${kubeconfig_path}")"
    scp "${remote_path}" "${kubeconfig_path}"
  else
    echo "Skipping kubeconfig fetch (--no-fetch); using existing ${kubeconfig_path}"
  fi

  # Two variants of the canonical per-side kubeconfig:
  #   - .generated/current.kubeconfig       proxy-url = http://127.0.0.1:${MCK_DEVC_PROXY_PORT}
  #     The host's view. site-context picks this when not inside the
  #     devcontainer. Patched unconditionally — host port is the
  #     gost-proxy loopback port from the /23 stack allocator (8000+index).
  #   - .generated/current.devc.kubeconfig  proxy-url = ${EVG_HOST_PROXY}
  #     The devcontainer's view. Written only when EVG_HOST_PROXY is set
  #     (we're inside the container or were handed it explicitly). Avoids
  #     a host-side run reverting an existing in-container patch.
  #
  # MCK_DEVC_PROXY_PORT is loaded from .devcontainer/.env by root-context.
  # Historical note: this used to be `80${MCK_DEVC_NET_PREFIX}` — fine in the
  # legacy /16 scheme where PREFIX was 16..31 (so the formula yielded 8016..
  # 8031), but under the /23 expansion PREFIX is the stack index 0..2047, so
  # any prefix >= 100 produced an out-of-range "port" like 80640. Use the
  # explicit PROXY_PORT var.
  : "${MCK_DEVC_PROXY_PORT:?MCK_DEVC_PROXY_PORT must be set (run \`make switch\` first to source .devcontainer/.env via root-context)}"
  host_proxy_port="${MCK_DEVC_PROXY_PORT}"
  echo "Patching kubeconfig with host-side proxy-url http://127.0.0.1:${host_proxy_port}"
  yq -i ".clusters[].cluster.proxy-url |= \"http://127.0.0.1:${host_proxy_port}\"" "${kubeconfig_path}"

  if [[ -n "${EVG_HOST_PROXY:-}" ]]; then
    devc_kubeconfig_path="${PROJECT_DIR}/.generated/current.devc.kubeconfig"
    echo "Writing devcontainer-side kubeconfig variant to ${devc_kubeconfig_path} (proxy ${EVG_HOST_PROXY})"
    cp "${kubeconfig_path}" "${devc_kubeconfig_path}"
    yq -i ".clusters[].cluster.proxy-url |= \"${EVG_HOST_PROXY}\"" "${devc_kubeconfig_path}"
  fi

  if [[ -n "${K8S_FWD_PROXY:-}" ]]; then
    # Inside the container we register the devc variant with the in-container
    # k8s-proxy (DNS + dynamic port-forwards for *.svc.cluster.local). On
    # the host the same K8S_FWD_PROXY var resolves to 127.0.0.1:11616 — a
    # daemon that may or may not be running locally. Best-effort: log the
    # outcome and continue, mirroring `.devcontainer/scripts/post-start.sh`.
    register_path="${devc_kubeconfig_path:-${kubeconfig_path}}"
    echo "Loading kubeconfig from ${register_path} onto ${K8S_FWD_PROXY}"
    curl --max-time 5 -fsS -X PATCH --data-binary @"${register_path}" "http://${K8S_FWD_PROXY}/kubeconfig" \
      && echo "registered kubeconfig with kfp at ${K8S_FWD_PROXY}" \
      || echo "kfp not reachable at ${K8S_FWD_PROXY}; skipping kubeconfig registration"
  fi
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
  # `setup_kind_cluster.sh` runs under root-context on the EVG host, where
  # KUBECONFIG resolves to ~/.kube/config. Its `-e` export step writes the
  # cluster's kubeconfig to ~/.kube/${cluster_name}. The subsequent
  # `get-kubeconfig` call expects the file at
  # ~/mongodb-kubernetes/.generated/current.kubeconfig (the canonical
  # per-side kubeconfig path). Copy it into place so the scp in
  # `get-kubeconfig` succeeds.
  ssh -T -q "${host_url}" \
    "mkdir -p ~/mongodb-kubernetes/.generated && cp -f ~/.kube/${cluster_name} ~/mongodb-kubernetes/.generated/current.kubeconfig"
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
  ssh "${host_url}" "$@"
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
  sync                                  rsync of project directory (.git is intentionally not synced)
  recreate-kind-cluster test-cluster    executes scripts/dev/recreate_kind_cluster.sh test-cluster and executes get-kubeconfig
  remote-prepare-local-e2e-run          executes prepare-local-e2e on the remote evg host
  get-kubeconfig                        copies remote ~/.kube/config locally to .generated/current.kubeconfig (and current.devc.kubeconfig when in devc)
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
get-kubeconfig) get-kubeconfig "$@" ;;
remote-prepare-local-e2e-run) remote-prepare-local-e2e-run ;;
ssh) ssh_to_host "$@" ;;
tunnel) retry_with_sleep tunnel "$@" ;;
sync) shift; sync "$@" ;;
cmd) cmd "$@" ;;
upload-my-ssh-private-key) upload-my-ssh-private-key ;;
help) usage ;;
*) usage ;;
esac
