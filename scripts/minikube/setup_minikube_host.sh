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

# Fix crictl for ppc64le kicbase images
fix_crictl_ppc64le() {
  if [[ "${ARCH}" == "ppc64le" ]]; then
    echo ">>> Applying ppc64le crictl fix for podman driver..."

    # Ensure crictl is available on host
    if ! command -v crictl &> /dev/null; then
      echo "❌ crictl not found on host - this is required for ppc64le fix"
      return 1
    fi

    # Wait for minikube container to be created
    local container_name="minikube"
    local max_wait=60
    local waited=0

    echo "Waiting for minikube container '${container_name}' to be ready..."
    while ! podman ps --format '{{.Names}}' | grep -q "^${container_name}$" && [[ $waited -lt $max_wait ]]; do
      sleep 2
      waited=$((waited + 2))
    done

    if [[ $waited -ge $max_wait ]]; then
      echo "⚠️  Timeout waiting for minikube container - crictl fix may be needed manually"
      return 1
    fi

    # Copy crictl from host to container using podman
    echo "Copying crictl to minikube container using podman..."
    if podman cp "$(which crictl)" "${container_name}:/usr/bin/crictl"; then
      podman exec "${container_name}" chmod +x /usr/bin/crictl
      echo "✅ crictl fix applied successfully with podman"

      # Verify the fix
      if podman exec "${container_name}" crictl --version &>/dev/null; then
        echo "✅ crictl is now working in minikube container"
        return 0
      else
        echo "⚠️  crictl copy succeeded but binary may not be working"
        return 1
      fi
    else
      echo "❌ Failed to copy crictl to container via podman"
      return 1
    fi
  fi
  return 0
}

# Start minikube with podman driver
start_minikube_cluster() {
  echo ">>> Starting minikube cluster with podman driver..."

  local start_args=("--driver=podman")

  # Add Calico CNI for better compatibility
  start_args+=("--cni=calico")

  # For ppc64le, we need to handle the crictl fix
  if [[ "${ARCH}" == "ppc64le" ]]; then
    echo "Starting minikube (ppc64le requires crictl fix for podman)..."

    # Start minikube in background to let container initialize
    timeout 120 "${PROJECT_DIR:-.}/bin/minikube" start "${start_args[@]}" &
    local minikube_pid=$!

    # Wait a bit for container to be created
    sleep 15

    # Apply crictl fix while minikube is starting
    if fix_crictl_ppc64le; then
      echo "✅ crictl fix applied, waiting for minikube to complete..."
    else
      echo "⚠️  crictl fix failed, but continuing..."
    fi

    # Wait for minikube to finish
    if wait $minikube_pid; then
      echo "✅ Minikube started successfully with crictl fix"
    else
      echo "❌ Minikube failed to start"
      return 1
    fi
  else
    # Standard minikube start
    if "${PROJECT_DIR:-.}/bin/minikube" start "${start_args[@]}"; then
      echo "✅ Minikube started successfully"
    else
      echo "❌ Minikube failed to start"
      return 1
    fi
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

  # Configure podman
  echo "Configuring Podman for ${ARCH}..."

  # Start podman service if not running
  systemctl --user enable podman.socket 2>/dev/null || true
  systemctl --user start podman.socket 2>/dev/null || true

  echo "✅ Podman configured successfully"
}

# Setup podman and container runtime
setup_podman
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

echo ""
echo ">>> Verifying crictl is available in PATH..."
if command -v crictl &> /dev/null; then
    crictl_version=$(crictl --version 2>/dev/null || echo "crictl available")
    crictl_path=$(which crictl 2>/dev/null || echo "unknown path")
    echo "✅ crictl found in PATH: ${crictl_version}"
    echo "✅ crictl location: ${crictl_path}"
else
    echo "❌ crictl not found in PATH - this may cause minikube issues"
    echo "Checking if crictl exists in project bin..."
    if [[ -f "${PROJECT_DIR:-.}/bin/crictl" ]]; then
        echo "Found crictl in project bin, installing to system directories..."
        sudo cp "${PROJECT_DIR:-.}/bin/crictl" /usr/local/bin/crictl
        sudo cp "${PROJECT_DIR:-.}/bin/crictl" /usr/bin/crictl
        sudo chmod +x /usr/local/bin/crictl
        sudo chmod +x /usr/bin/crictl
        echo "✅ crictl installed to /usr/local/bin/ and /usr/bin/"

        # Force PATH refresh and verify
        hash -r 2>/dev/null || true
        if command -v crictl &> /dev/null; then
            echo "✅ crictl now available: $(which crictl)"
        else
            echo "⚠️  crictl installation may not be working properly"
        fi
    else
        echo "❌ crictl not found in project bin either"
    fi
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
echo "CNI: Calico"
if [[ "${ARCH}" == "ppc64le" ]]; then
    echo "Special Config: ppc64le crictl fix applied for podman"
fi
