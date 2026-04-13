#!/usr/bin/env bash

# Consolidated setup script for minikube host with multi-architecture support
# This script groups all the setup steps needed for IBM machines and other architectures
# Can be run on static hosts for testing and verification

source scripts/dev/set_env_context.sh
source scripts/funcs/install
set -Eeou pipefail

echo "=========================================="
echo "Setting up minikube host with multi-architecture support"
echo "Architecture: $(detect_architecture)"
echo "OS: $(uname -s)"
echo "=========================================="

# Function to run a setup step with error handling and logging
run_setup_step() {
    local step_name="$1"
    shift
    local script_command=("$@")

    echo ""
    echo ">>> Running: ${step_name}"
    echo ">>> Command: ${script_command[*]}"

    local script_path="${script_command[0]}"
    if [[ -f "${script_path}" ]]; then
        if "${script_command[@]}"; then
            echo "✅ ${step_name} completed successfully"
        else
            echo "❌ ${step_name} failed"
            exit 1
        fi
    else
        echo "❌ Script not found: ${script_path}"
        exit 1
    fi
}

# Install libsodium from source on s390x — RHEL9 ships 1.0.18 which is too old for pynacl>=1.6
if [[ "$(uname -m)" == "s390x" ]]; then
    echo ""
    echo ">>> Running: libsodium installation (s390x only)"
    LIBSODIUM_VERSION="1.0.20"
    if pkg-config --exists libsodium 2>/dev/null && [[ "$(pkg-config --modversion libsodium)" == "${LIBSODIUM_VERSION}" ]]; then
        echo "✅ libsodium ${LIBSODIUM_VERSION} already installed, skipping installation"
    else
        echo ">>> Building libsodium ${LIBSODIUM_VERSION} from source..."
        tmp_dir=$(mktemp -d)
        curl --fail --retry 3 -L -o "${tmp_dir}/libsodium.tar.gz" \
            "https://download.libsodium.org/libsodium/releases/libsodium-${LIBSODIUM_VERSION}.tar.gz"
        tar xzf "${tmp_dir}/libsodium.tar.gz" -C "${tmp_dir}"
        (cd "${tmp_dir}/libsodium-${LIBSODIUM_VERSION}" && ./configure --prefix=/usr/local && make -j"$(nproc)" && sudo make install)
        # Ensure /usr/local/lib is in ldconfig search paths (RHEL9 doesn't include it by default)
        echo "/usr/local/lib" | sudo tee /etc/ld.so.conf.d/local.conf
        sudo ldconfig
        rm -rf "${tmp_dir}"
        echo "✅ libsodium ${LIBSODIUM_VERSION} installed successfully"
    fi
fi

# Setup Python environment (needed for AWS CLI pip installation)
export GRPC_PYTHON_BUILD_SYSTEM_OPENSSL=1
run_setup_step "Python Virtual Environment" "scripts/dev/recreate_python_venv.sh"

run_setup_step "AWS CLI Setup" "scripts/evergreen/setup_aws.sh"

run_setup_step "kubectl and helm Setup" "scripts/evergreen/setup_kubectl.sh"

run_setup_step "jq Setup" "scripts/evergreen/setup_jq.sh"

run_setup_step "IBM Container Runtime Setup" "scripts/dev/setup_ibm_container_runtime.sh"

if [[ "${SKIP_MINIKUBE_SETUP:-}" != "true" ]]; then
  run_setup_step "Minikube Host Setup with Container Runtime Detection" "scripts/minikube/setup_minikube.sh"
else
  echo "⏭️ Skipping Minikube setup as SKIP_MINIKUBE_SETUP=true"
fi
export CONTAINER_RUNTIME=podman
run_setup_step "Container Registry Authentication" "scripts/dev/configure_container_auth.sh"

echo ""
echo "=========================================="
echo "✅ host setup completed successfully!"
echo "=========================================="
echo ""
echo "Installed tools summary:"
echo "Container Runtime: podman"
echo "- Python: $(python --version 2>/dev/null || python3 --version 2>/dev/null || echo 'Not found')"
echo "- AWS CLI: $(aws --version 2>/dev/null || echo 'Not found')"
echo "- kubectl: $(kubectl version --client 2>/dev/null || echo 'Not found')"
echo "- helm: $(helm version --short 2>/dev/null || echo 'Not found')"
echo "- jq: $(jq --version 2>/dev/null || echo 'Not found')"
echo ""
echo "Setup complete! Host is ready for operations."
