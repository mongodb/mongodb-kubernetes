#!/usr/bin/env bash

# this script downloads necessary tooling for alternative architectures (s390x, ppc64le) using minikube (similar to setup_evg_host.sh)
source scripts/dev/set_env_context.sh
source scripts/funcs/install


set -Eeou pipefail

set_limits() {
  echo "Increasing fs.inotify.max_user_instances"
  sudo sysctl -w fs.inotify.max_user_instances=8192

  echo "Increasing fs.inotify.max_user_watches"
  sudo sysctl -w fs.inotify.max_user_watches=10485760

  echo "Increasing the number of open files and processes"
  nofile_max=$(cat /proc/sys/fs/nr_open)
  nproc_max=65536
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

  echo "Setting kernel thread limits"
  sudo sysctl -w kernel.threads-max=131072
  sudo sysctl -w kernel.pid_max=131072
  sudo sysctl -w vm.max_map_count=262144
}

# retrieve arch variable off the shell command line
ARCH="$(detect_architecture)"

echo "Setting up minikube host for architecture: ${ARCH}"

download_minikube() {
  echo "Downloading minikube for ${ARCH}..."
  scripts/minikube/install_minikube.sh
}

# Start minikube with podman driver (rootful mode for reliable networking)
start_minikube_cluster() {
  echo ">>> Starting minikube cluster with podman driver (rootful mode)..."

  # Use a root-owned TMPDIR to avoid juju lock file permission issues
  # See: https://github.com/kubernetes/minikube/issues/6391
  local MINIKUBE_TMPDIR="/root/.minikube-tmp"
  sudo rm -rf "${MINIKUBE_TMPDIR}" 2>/dev/null || true
  sudo mkdir -p "${MINIKUBE_TMPDIR}"

  # Always clean up first to avoid stale cluster-scoped resources from previous runs
  echo "Cleaning up any existing minikube state..."
  sudo rm -rf ~/.minikube/machines/minikube 2>/dev/null || true
  rm -rf ~/.minikube/machines/minikube 2>/dev/null || true

  echo "Ensuring clean minikube state..."
  sudo TMPDIR="${MINIKUBE_TMPDIR}" "${PROJECT_DIR:-.}/bin/minikube" delete 2>/dev/null || true
  "${PROJECT_DIR:-.}/bin/minikube" delete 2>/dev/null || true

  echo "Cleaning up stale podman volumes..."
  sudo podman volume rm -f minikube 2>/dev/null || true
  sudo podman network rm -f minikube 2>/dev/null || true

  echo "Cleaning up juju lock files (prevents permission issues with mixed user/root runs)..."
  sudo rm -f /tmp/juju-mk* 2>/dev/null || true

  # Use rootful podman - rootless has iptables/CNI issues on ppc64le and s390x
  # --force allows running minikube with root privileges
  local start_args=("--driver=podman" "--container-runtime=containerd" "--force")
  start_args+=("--cpus=4" "--memory=8g")
  start_args+=("--cni=bridge")

  echo "Starting minikube with args: ${start_args[*]}"
  if sudo TMPDIR="${MINIKUBE_TMPDIR}" "${PROJECT_DIR:-.}/bin/minikube" start "${start_args[@]}"; then
    echo "✅ Minikube started successfully"
  else
    echo "❌ Minikube failed to start"
    echo "Minikube logs:"
    sudo TMPDIR="${MINIKUBE_TMPDIR}" "${PROJECT_DIR:-.}/bin/minikube" logs | tail -20
    return 1
  fi
}

set_limits
download_minikube

echo ""
echo ">>> Verifying minikube installation..."
if command -v minikube &> /dev/null; then
    minikube_version=$(minikube version --short 2>/dev/null || minikube version 2>/dev/null | head -n1)
    echo "✅ Minikube installed successfully: ${minikube_version}"
else
    echo "❌ Minikube installation failed - minikube command not found"
    echo "Please check the installation logs above for errors"
    exit 1
fi

# Start the minikube cluster
start_minikube_cluster

# Update kubectl context to point to the running cluster
echo ""
echo ">>> Updating kubectl context for minikube cluster..."
sudo TMPDIR="/root/.minikube-tmp" "${PROJECT_DIR:-.}/bin/minikube" update-context

# Export flattened kubeconfig to current user (minikube runs as root, but e2e tests run as user)
# --flatten embeds certs inline so user doesn't need access to /root/.minikube/
echo ">>> Exporting kubeconfig to current user..."
mkdir -p "${HOME}/.kube"
# - sudo command | tee file - the pipe breaks the sudo context, so tee runs as the current user
# - This avoids SC2024 because there's no redirect directly after sudo
sudo TMPDIR="/root/.minikube-tmp" "${PROJECT_DIR:-.}/bin/minikube" kubectl -- config view --flatten | tee "${HOME}/.kube/config" > /dev/null
chmod 600 "${HOME}/.kube/config"
echo "✅ Kubectl context updated successfully"

echo "Minikube host setup completed successfully for ${ARCH}!"

# Final status
echo ""
echo "=========================================="
echo "✅ Setup Summary"
echo "=========================================="
echo "Architecture: ${ARCH}"
echo "Minikube Driver: podman"
echo "Minikube: Default cluster"
echo "Minikube: ${minikube_version}"
echo "CNI: bridge (default)"
