#!/usr/bin/env bash
set -Eeou pipefail

source scripts/dev/set_env_context.sh
source scripts/funcs/install

# Detect architecture
ARCH=$(uname -m)
case "${ARCH}" in
    x86_64)
        MINIKUBE_ARCH="amd64"
        ;;
    aarch64)
        MINIKUBE_ARCH="arm64"
        ;;
    ppc64le)
        MINIKUBE_ARCH="ppc64le"
        ;;
    s390x)
        MINIKUBE_ARCH="s390x"
        ;;
    *)
        echo "Error: Unsupported architecture: ${ARCH}"
        echo "Supported architectures: x86_64, aarch64, ppc64le, s390x"
        exit 1
        ;;
esac

echo "Installing minikube on ${ARCH} architecture..."

# Install crictl (container runtime CLI)
echo "Installing crictl for ${ARCH}..."
CRICTL_VERSION=$(curl -s https://api.github.com/repos/kubernetes-sigs/cri-tools/releases/latest | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')

# Download and extract crictl tar.gz
mkdir -p "${PROJECT_DIR:-.}/bin"
CRICTL_URL="https://github.com/kubernetes-sigs/cri-tools/releases/download/${CRICTL_VERSION}/crictl-${CRICTL_VERSION}-linux-${MINIKUBE_ARCH}.tar.gz"
echo "Downloading ${CRICTL_URL}"
TEMP_DIR=$(mktemp -d)
curl --retry 3 --silent -L "${CRICTL_URL}" -o "${TEMP_DIR}/crictl.tar.gz"
tar -xzf "${TEMP_DIR}/crictl.tar.gz" -C "${TEMP_DIR}/"
chmod +x "${TEMP_DIR}/crictl"
mv "${TEMP_DIR}/crictl" "${PROJECT_DIR:-.}/bin/crictl"
rm -rf "${TEMP_DIR}"
echo "Installed crictl to ${PROJECT_DIR:-.}/bin"

# Also install crictl system-wide so minikube can find it
echo "Installing crictl system-wide..."
if [[ -f "${PROJECT_DIR:-.}/bin/crictl" ]]; then
    # Install to both /usr/local/bin and /usr/bin for better PATH coverage
    sudo cp "${PROJECT_DIR:-.}/bin/crictl" /usr/local/bin/crictl
    sudo cp "${PROJECT_DIR:-.}/bin/crictl" /usr/bin/crictl
    sudo chmod +x /usr/local/bin/crictl
    sudo chmod +x /usr/bin/crictl
    echo "✅ crictl installed to /usr/local/bin/ and /usr/bin/"
    
    # Verify installation
    if command -v crictl >/dev/null 2>&1; then
        echo "✅ crictl is now available in PATH: $(which crictl)"
        echo "✅ crictl version: $(crictl --version 2>/dev/null || echo 'version check failed')"
    else
        echo "⚠️  crictl installed but not found in PATH"
    fi
else
    echo "⚠️  crictl not found in project bin, minikube may have issues"
fi

# Install minikube
echo "Installing minikube for ${ARCH}..."
MINIKUBE_VERSION=$(curl -s https://api.github.com/repos/kubernetes/minikube/releases/latest | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')

# Download minikube for detected architecture
download_and_install_binary "${PROJECT_DIR:-.}/bin" minikube "https://github.com/kubernetes/minikube/releases/download/${MINIKUBE_VERSION}/minikube-linux-${MINIKUBE_ARCH}"

echo "Crictl ${CRICTL_VERSION} and Minikube ${MINIKUBE_VERSION} installed successfully for ${ARCH}"
