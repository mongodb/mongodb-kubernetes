#!/usr/bin/env bash

# this script downloads necessary tooling in EVG host

set -Eeou pipefail

source scripts/funcs/install

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

# Detect architecture from the environment
ARCH=$(detect_architecture)
echo "Detected architecture: ${ARCH}"

download_kind() {
  scripts/evergreen/setup_kind.sh /usr/local
}

download_kubectl() {
  kubectl_version=$(curl --retry 5 -L -v https://dl.k8s.io/release/stable.txt)
  echo "Downloading kubectl ${kubectl_version}..."

  curl --retry 5 -o kubectl -L -v "https://dl.k8s.io/release/${kubectl_version}/bin/linux/${ARCH}/kubectl"
  chmod +x kubectl
  sudo mv kubectl /usr/local/bin/kubectl
}

download_helm() {
  echo "Downloading helm..."
  curl -s -o helm.tar.gz -L https://get.helm.sh/helm-v3.17.1-linux-"${ARCH}"tar.gz
  tar -xf helm.tar.gz 2>/dev/null
  sudo mv linux-"${ARCH}"helm /usr/local/bin/helm
  rm helm.tar.gz
  rm -rf linux-"${ARCH}/"
}

set_limits
download_kind &
download_kubectl &
download_helm &

AUTO_RECREATE=${1:-false}
if [[ "${AUTO_RECREATE}" == "true" ]]; then
  set_auto_recreate &
fi

wait
