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

# Setup local registry and build custom kicbase image for ppc64le with crictl
setup_local_registry_and_custom_image() {
  if [[ "${ARCH}" == "ppc64le" ]]; then
    echo ">>> Setting up local registry and custom kicbase image for ppc64le..."

    # Check if local registry is running (use 127.0.0.1 to avoid IPv6 fallback delay)
    registry_running=false
    if curl -s --max-time 5 http://127.0.0.1:5000/v2/_catalog >/dev/null 2>&1; then
      echo "Registry detected via HTTP check"
      registry_running=true
    fi

    if ! ${registry_running}; then
      echo "Starting local container registry on port 5000..."

      # Clean up any existing registry first
      sudo podman rm -f registry 2>/dev/null || true

      if ! sudo podman run -d -p 127.0.0.1:5000:5000 --name registry --restart=always docker.io/library/registry:2; then
        echo "❌ Failed to start local registry - trying alternative approach"
        exit 1
      fi

      # Wait for registry to be ready
      echo "Waiting for registry to be ready..."
      for _ in {1..30}; do
        if curl -s --max-time 5 http://127.0.0.1:5000/v2/_catalog >/dev/null 2>&1; then
          break
        fi
        sleep 1
      done
    else
      echo "✅ Local registry already running"
    fi

    # Configure podman to trust local registry (rootful only since minikube uses sudo podman)
    echo "Configuring registries.conf to trust local registry..."
    sudo mkdir -p /root/.config/containers
    sudo tee /root/.config/containers/registries.conf << 'EOF' >/dev/null
[[registry]]
location = "localhost:5000"
insecure = true
EOF
    echo "✅ Registry configuration created"

    custom_image_tag="localhost:5000/kicbase:v0.0.48"
    if curl -s --max-time 5 http://127.0.0.1:5000/v2/kicbase/tags/list | grep -q "v0.0.48"; then
      echo "Custom kicbase image already exists in local registry"
      return 0
    fi

    # Build custom kicbase image with crictl
    echo "Building custom kicbase image with crictl for ppc64le..."

    # Build custom kicbase image
    mkdir -p "${PROJECT_DIR:-.}/scripts/minikube/kicbase"
    cat > "${PROJECT_DIR:-.}/scripts/minikube/kicbase/Dockerfile" << 'EOF'
FROM gcr.io/k8s-minikube/kicbase:v0.0.48
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

    # Use 127.0.0.1 to avoid IPv6 issues with podman on ppc64le, we might bind to ipv6 and it might not work
    if ! curl -s --max-time 5 http://127.0.0.1:5000/v2/_catalog >/dev/null 2>&1; then
      echo "Registry not responding, restarting..."
      sudo podman rm -f registry 2>/dev/null || true
      sudo podman run -d -p 127.0.0.1:5000:5000 --name registry --restart=always docker.io/library/registry:2
      for _ in {1..15}; do
        if curl -s --max-time 5 http://127.0.0.1:5000/v2/_catalog >/dev/null 2>&1; then
          echo "Registry restarted successfully"
          break
        fi
        sleep 1
      done
    fi

    sudo podman push "${custom_image_tag}" --tls-verify=false || {
      echo "Failed to push to registry"
      return 1
    }
    cd - >/dev/null
    echo "Custom kicbase image ready: ${custom_image_tag}"
  fi
  return 0
}

# Start minikube with podman driver (rootful mode for reliable networking)
start_minikube_cluster() {
  echo ">>> Starting minikube cluster with podman driver (rootful mode)..."

  if "${PROJECT_DIR:-.}/bin/minikube" status &>/dev/null; then
    echo "✅ Minikube is already running - verifying health..."
    if "${PROJECT_DIR:-.}/bin/minikube" kubectl -- get nodes &>/dev/null; then
      echo "✅ Minikube cluster is healthy - skipping setup"
      return 0
    else
      echo "⚠️ Minikube running but unhealthy - will recreate"
    fi
  fi

  # Clean up any existing minikube state to avoid cached configuration issues
  echo "Cleaning up any existing minikube state..."
  if [[ -d ~/.minikube/machines/minikube ]]; then
    echo "Removing ~/.minikube/machines/minikube directory..."
    rm -rf ~/.minikube/machines/minikube
  fi

  echo "Ensuring clean minikube state..."
  "${PROJECT_DIR:-.}/bin/minikube" delete 2>/dev/null || true

  # Clean up stale podman volumes
  echo "Cleaning up stale podman volumes..."
  sudo podman volume rm -f minikube 2>/dev/null || true
  sudo podman network rm -f minikube 2>/dev/null || true

  # Use rootful podman - rootless has iptables/CNI issues on ppc64le and s390x
  local start_args=("--driver=podman" "--container-runtime=containerd" "--rootless=false")
  start_args+=("--cpus=4" "--memory=8g")

  if [[ "${ARCH}" == "ppc64le" ]]; then
    echo "Using custom kicbase image for ppc64le with crictl..."

    start_args+=("--base-image=localhost:5000/kicbase:v0.0.48")
    start_args+=("--insecure-registry=localhost:5000")
    # Use bridge CNI for ppc64le - kindnet doesn't have ppc64le images
    start_args+=("--cni=bridge")
  elif [[ "${ARCH}" == "s390x" ]]; then
    # Use bridge CNI for s390x to avoid potential image availability issues
    start_args+=("--cni=bridge")
  fi

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
echo "Minikube Driver: podman"
echo "Minikube: Default cluster"
echo "Minikube: ${minikube_version}"
echo "CNI: bridge (default)"
if [[ "${ARCH}" == "ppc64le" ]]; then
    echo "Special Config: Custom kicbase image with crictl via local registry"
fi
