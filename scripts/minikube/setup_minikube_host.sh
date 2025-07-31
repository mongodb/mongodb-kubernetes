#!/usr/bin/env bash

# this script downloads necessary tooling for alternative architectures (s390x, ppc64le) using minikube (similar to setup_evg_host.sh)
source scripts/dev/set_env_context.sh

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

# Setup local registry and build custom kicbase image for ppc64le with crictl
setup_local_registry_and_custom_image() {
  if [[ "${ARCH}" == "ppc64le" ]]; then
    echo ">>> Setting up local registry and custom kicbase image for ppc64le..."
    
    # Check if local registry is running
    if ! podman ps --filter "name=registry" --format "{{.Names}}" | grep -q "^registry$"; then
      echo "Starting local container registry on port 5000..."
      podman run -d -p 5000:5000 --name registry --restart=always docker.io/library/registry:2 || {
        echo "Registry might already exist, trying to start it..."
        podman start registry || {
          echo "Removing existing registry and creating new one..."
          podman rm -f registry 2>/dev/null || true
          podman run -d -p 5000:5000 --name registry --restart=always docker.io/library/registry:2
        }
      }
      
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
    
    # Configure per-user podman to trust local registry (no system-wide changes)
    echo "Configuring per-user podman registry settings..."
    mkdir -p ~/.config/containers
    
    # Create user-specific registries.conf that includes insecure local registry
    cat > ~/.config/containers/registries.conf << 'EOF'
unqualified-search-registries = ["registry.access.redhat.com", "registry.redhat.io", "docker.io"]

[[registry]]
location = "localhost:5000"
insecure = true

short-name-mode = "permissive"
EOF
    
    # Check if custom image already exists in local registry
    if curl -s http://localhost:5000/v2/kicbase/tags/list | grep -q "v0.0.47"; then
      echo "✅ Custom kicbase image already exists in local registry"
      return 0
    fi
    
    # Build custom kicbase image with crictl
    echo "Building custom kicbase image with crictl for ppc64le..."
    
    # Create build directory if it doesn't exist
    mkdir -p "${PROJECT_DIR:-.}/scripts/minikube/kicbase"
    
    # Create Dockerfile for custom kicbase
    cat > "${PROJECT_DIR:-.}/scripts/minikube/kicbase/Dockerfile" << 'EOF'
FROM gcr.io/k8s-minikube/kicbase:v0.0.47

# Install crictl for ppc64le if needed
RUN if [ "$(uname -m)" = "ppc64le" ]; then \
        echo "Installing crictl for ppc64le architecture..." && \
        CRICTL_VERSION="v1.28.0" && \
        curl -L "https://github.com/kubernetes-sigs/cri-tools/releases/download/${CRICTL_VERSION}/crictl-${CRICTL_VERSION}-linux-ppc64le.tar.gz" \
            -o /tmp/crictl.tar.gz && \
        tar -C /usr/bin -xzf /tmp/crictl.tar.gz && \
        chmod +x /usr/bin/crictl && \
        rm /tmp/crictl.tar.gz && \
        echo "crictl installed successfully" && \
        crictl --version; \
    else \
        echo "Not ppc64le architecture, skipping crictl installation"; \
    fi

# Verify crictl is available
RUN command -v crictl >/dev/null 2>&1 && echo "crictl is available" || echo "crictl not found"
EOF
    
    # Build and push to local registry
    echo "Building custom kicbase image..."
    cd "${PROJECT_DIR:-.}/scripts/minikube/kicbase"
    
    podman build -t localhost:5000/kicbase:v0.0.47 .
    
    echo "Pushing custom image to local registry..."
    podman push localhost:5000/kicbase:v0.0.47 --tls-verify=false
    
    cd - > /dev/null
    
    echo "✅ Custom kicbase image with crictl ready in local registry"
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
  
  # Delete any existing minikube cluster to start fresh
  echo "Ensuring clean minikube state..."
  "${PROJECT_DIR:-.}/bin/minikube" delete 2>/dev/null || true

  local start_args=("--driver=podman")

  # Use custom kicbase image for ppc64le with crictl included
  if [[ "${ARCH}" == "ppc64le" ]]; then
    echo "Using custom kicbase image for ppc64le with crictl..."
    start_args+=("--base-image=localhost:5000/kicbase:v0.0.47")
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

  # Check if podman is already available
  if command -v podman &> /dev/null; then
    echo "✅ Podman already installed"
  else
    echo "Installing podman..."
    sudo dnf install -y podman
  fi

  # Configure podman for CI environment
  echo "Configuring Podman for ${ARCH} CI environment..."

  # Enable lingering for the current user to fix systemd session issues
  current_user=$(whoami)
  current_uid=$(id -u)
  echo "Enabling systemd lingering for user ${current_user} (UID: ${current_uid})"
  sudo loginctl enable-linger "${current_uid}" 2>/dev/null || true

  # Configure podman to use cgroupfs instead of systemd in CI
  mkdir -p ~/.config/containers
  cat > ~/.config/containers/containers.conf << EOF
[containers]
cgroup_manager = "cgroupfs"
events_logger = "file"

[engine]
cgroup_manager = "cgroupfs"
EOF

  # Start podman service if not running
  systemctl --user enable podman.socket 2>/dev/null || true
  systemctl --user start podman.socket 2>/dev/null || true

  echo "✅ Podman configured successfully for CI"
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

echo ""
echo ">>> Verifying podman installation..."
if command -v podman &> /dev/null; then
    podman_version=$(podman --version 2>/dev/null)
    echo "✅ Podman installed successfully: ${podman_version}"

    # Check podman info
    if podman info &>/dev/null; then
        echo "✅ Podman is working correctly"
    else
        echo "⚠️  Podman may need additional configuration"
    fi
else
    echo "❌ Podman installation failed - podman command not found"
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
echo "Podman: ${podman_version}"
echo "CNI: bridge (default)"
if [[ "${ARCH}" == "ppc64le" ]]; then
    echo "Special Config: Custom kicbase image with crictl via local registry"
fi
