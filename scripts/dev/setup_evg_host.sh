#!/usr/bin/env bash

# this script downloads necessary tooling in EVG host

set -Eeou pipefail

source scripts/funcs/install
source scripts/funcs/printing
source scripts/dev/set_env_context.sh

set_limits() {
  echo "Increasing fs.inotify.max_user_instances"
  sudo sysctl -w fs.inotify.max_user_instances=8192

  echo "Increasing fs.inotify.max_user_watches"
  sudo sysctl -w fs.inotify.max_user_watches=10485760

  echo "Increasing the number of open files"
  nofile_max=$(cat /proc/sys/fs/nr_open)
  nproc_max=$(ulimit -u)
  sudo tee -a /etc/security/limits.conf <<EOF
  root hard nofile ${nofile_max}
  root hard nproc ${nproc_max}
  root soft nofile ${nofile_max}
  root soft nproc ${nproc_max}
  * hard nofile ${nofile_max}
  * hard nproc ${nproc_max}
  * soft nofile ${nofile_max}
  * soft nproc ${nproc_max}
EOF
}

set_auto_recreate() {
  echo "Creating systemd service for recreating kind clusters on reboot"

  sudo cp /home/ubuntu/mongodb-kubernetes/scripts/dev/kindclusters.service /etc/systemd/system/kindclusters.service
  sudo systemctl enable kindclusters.service
}

set_limits | prepend "set_limits"

# download_kube_tools already installed kind, kubectl and helm into ${PROJECT_DIR}/bin,
# which is on PATH for this function. Re-running the same installers is idempotent and
# repairs a partial install, so this script downloads nothing itself.
scripts/evergreen/setup_kind.sh | prepend "download_kind" &
kind_pid=$!
scripts/evergreen/setup_kubectl.sh | prepend "download_kubectl" &
kubectl_pid=$!

AUTO_RECREATE=${1:-false}
if [[ "${AUTO_RECREATE}" == "true" ]]; then
  set_auto_recreate | prepend "set_auto_recreate" &
  auto_recreate_pid=$!
fi

# Explicit waits. A bare `wait` returns 0 even when a background job failed, which is
# how the helm download stayed broken here without anyone noticing.
wait "${kind_pid}" || { echo "kind installation failed" >&2; exit 1; }
wait "${kubectl_pid}" || { echo "kubectl and helm installation failed" >&2; exit 1; }
if [[ -n "${auto_recreate_pid:-}" ]]; then
  wait "${auto_recreate_pid}" || { echo "auto recreate setup failed" >&2; exit 1; }
fi
