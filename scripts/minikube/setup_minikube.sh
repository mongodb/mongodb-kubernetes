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
ARCH=${1-"$(detect_architecture)"}

echo "Setting up minikube host for architecture: ${ARCH}"

download_minikube() {
  echo "Downloading minikube for ${ARCH}..."
  scripts/minikube/install_minikube.sh
}

# Setup local registry and build custom kicbase image for ppc64le with crictl
setup_local_registry_and_custom_image() {
  if [[ "${ARCH}" == "ppc64le" ]]; then
    echo ">>> Setting up local registry and custom kicbase image for ppc64le..."

    # Check if local registry is running (with fallback for namespace issues)
    registry_running=false
    if curl -s http://localhost:5000/v2/_catalog >/dev/null 2>&1; then
      echo "Registry detected via HTTP check (podman ps failed)"
      registry_running=true
    fi

    if ! ${registry_running}; then
      echo "Starting local container registry on port 5000..."

      # Clean up any existing registry first
      sudo podman rm -f registry 2>/dev/null || true

      if ! sudo podman run -d -p 5000:5000 --name registry --restart=always docker.io/library/registry:2; then
        echo "❌ Failed to start local registry - trying alternative approach"
        exit 1
      fi

      # Wait for registry to be ready
      echo "Waiting for registry to be ready..."
      for i in {1..30}; do
        if curl -s http://localhost:5000/v2/_catalog >/dev/null 2>&1; then
          break
        fi
        sleep 1
      done
    else
      echo "✅ Local registry already running"
    fi

    # Configure podman to trust local registry (both user and root level for minikube)
    echo "Configuring registries.conf to trust local registry..."

    # User-level config
    mkdir -p ~/.config/containers
    cat > ~/.config/containers/registries.conf << 'EOF'
[[registry]]
location = "localhost:5000"
insecure = true
EOF

    # Root-level config (since minikube uses sudo podman)
    sudo mkdir -p /root/.config/containers
    sudo tee /root/.config/containers/registries.conf << 'EOF' >/dev/null
[[registry]]
location = "localhost:5000"
insecure = true
EOF

    echo "✅ Registry configuration created for both user and root"
    custom_image_tag="localhost:5000/kicbase:v0.0.47"

    # Determine image tag
    custom_image_tag="localhost:5000/kicbase:v0.0.47"
    if curl -s http://localhost:5000/v2/kicbase/tags/list | grep -q "v0.0.47"; then
      echo "Custom kicbase image already exists in local registry"
      return 0
    fi

    # Build custom kicbase image with crictl
    echo "Building custom kicbase image with crictl for ppc64le..."

    # Build custom kicbase image
    mkdir -p "${PROJECT_DIR:-.}/scripts/minikube/kicbase"
    cat > "${PROJECT_DIR:-.}/scripts/minikube/kicbase/Dockerfile" << 'EOF'
FROM gcr.io/k8s-minikube/kicbase:v0.0.47
RUN if [ "$(uname -m)" = "ppc64le" ]; then \
        CRICTL_VERSION="v1.28.0" && \
        curl -L "https://github.com/kubernetes-sigs/cri-tools/releases/download/${CRICTL_VERSION}/crictl-${CRICTL_VERSION}-linux-ppc64le.tar.gz" \
            -o /tmp/crictl.tar.gz && \
        tar -C /usr/bin -xzf /tmp/crictl.tar.gz && \
        chmod +x /usr/bin/crictl && \
        rm /tmp/crictl.tar.gz; \
    fi
EOF

    cd "${PROJECT_DIR:-.}/scripts/minikube/kicbase"
    sudo podman build -t "${custom_image_tag}" . || {
      echo "Failed to build custom image"
      return 1
    }
    sudo podman push "${custom_image_tag}" --tls-verify=false || {
      echo "Failed to push to registry"
      return 1
    }
    cd - >/dev/null
    echo "Custom kicbase image ready: ${custom_image_tag}"
  fi
  return 0
}

# Start minikube with podman driver
start_minikube_cluster() {
  echo ">>> Starting minikube cluster with podman driver..."

  # Clean up any existing minikube state to avoid cached configuration issues
  echo "Cleaning up any existing minikube state..."
  if [[ -d ~/.minikube/machines/minikube ]]; then
    echo "Removing ~/.minikube/machines/minikube directory..."
    rm -rf ~/.minikube/machines/minikube
  fi

  echo "Ensuring clean minikube state..."
  "${PROJECT_DIR:-.}/bin/minikube" delete 2>/dev/null || true

  local start_args=("--driver=podman")

  if [[ "${ARCH}" == "ppc64le" ]]; then
    echo "Using custom kicbase image for ppc64le with crictl..."

    start_args+=("--base-image=localhost:5000/kicbase:v0.0.47")
    start_args+=("--insecure-registry=localhost:5000")
  fi

  # Use default bridge CNI to avoid Docker Hub rate limiting issues
  # start_args+=("--cni=bridge")

  echo "Starting minikube with args: ${start_args[*]}"
  if "${PROJECT_DIR:-.}/bin/minikube" start "${start_args[@]}"; then
    echo "✅ Minikube started successfully"
  else
    echo "❌ Minikube failed to start"
    echo "Minikube logs:"
    "${PROJECT_DIR:-.}/bin/minikube" logs | tail -20
    return 1
  fi
}

setup_podman() {
  echo "Setting up podman for ${ARCH}..."

  # Configure podman to use cgroupfs instead of systemd in CI
  mkdir -p ~/.config/containers
  cat > ~/.config/containers/containers.conf << EOF
[containers]
cgroup_manager = "cgroupfs"
events_logger = "file"

[engine]
cgroup_manager = "cgroupfs"
EOF

}

# Setup podman and container runtime
setup_podman
set_limits
download_minikube

# Setup local registry and custom kicbase image for ppc64le if needed
setup_local_registry_and_custom_image

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

if [[ "${ARCH}" == "ppc64le" ]]; then
    echo ""
    echo ">>> Note: crictl will be patched into the minikube container after startup"
else
    echo ""
    echo ">>> Using standard kicbase image (crictl included for x86_64/aarch64/s390x)"
fi

# Start the minikube cluster
start_minikube_cluster

# Update kubectl context to point to the running cluster
echo ""
echo ">>> Updating kubectl context for minikube cluster..."
"${PROJECT_DIR:-.}/bin/minikube" update-context
echo "✅ Kubectl context updated successfully"

echo "Minikube host setup completed successfully for ${ARCH}!"

# Final status
echo ""
echo "=========================================="
echo "✅ Setup Summary"
echo "=========================================="
echo "Architecture: ${ARCH}"
echo "Container Runtime: podman"
echo "Minikube Driver: podman"
echo "Minikube: Default cluster"
echo "Minikube: ${minikube_version}"
echo "CNI: bridge (default)"
if [[ "${ARCH}" == "ppc64le" ]]; then
    echo "Special Config: Custom kicbase image with crictl via local registry"
fi
