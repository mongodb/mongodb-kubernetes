#!/usr/bin/env bash

# this script downloads necessary tooling for alternative architectures (s390x, ppc64le) using minikube (similar to setup_evg_host.sh)
source scripts/dev/set_env_context.sh

set -Eeou pipefail

check_disk_space() {
  echo "Checking available disk space..."
  local available_gb
  available_gb=$(df / | awk 'NR==2 {print int($4/1024/1024)}')

  if [[ $available_gb -lt 5 ]]; then
    echo "ERROR: Insufficient disk space. Available: ${available_gb}GB, Required: 5GB minimum"
    echo "Please clean up disk space before continuing:"
    echo "  sudo dnf clean all"
    echo "  sudo rm -rf /var/cache/dnf/* /tmp/* /var/tmp/*"
    echo "  docker system prune -af"
    return 1
  fi

  echo "Disk space check passed: ${available_gb}GB available"
}

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

# retrieve arch variable off the shell command line
ARCH=${1-"$(uname -m)"}

# Validate architecture
case "${ARCH}" in
  s390x|ppc64le|x86_64|aarch64)
    echo "Setting up minikube host for architecture: ${ARCH}"
    ;;
  *)
    echo "ERROR: Unsupported architecture: ${ARCH}"
    echo "Supported architectures: s390x, ppc64le, x86_64, aarch64"
    exit 1
    ;;
esac

download_minikube() {
  echo "Downloading minikube for ${ARCH}..."
  scripts/minikube/install-minikube.sh
}

download_docker() {
  echo "Installing Docker for ${ARCH}..."
  scripts/minikube/install-docker.sh
}

check_disk_space
set_limits
download_docker &
download_minikube &

wait

echo "Minikube host setup completed successfully for ${ARCH}!"

# Final status
echo ""
echo "=========================================="
echo "✅ Setup Summary"
echo "=========================================="
echo "Architecture: ${ARCH}"
echo "Minikube Profile: ${MINIKUBE_PROFILE:-mongodb-e2e}"
