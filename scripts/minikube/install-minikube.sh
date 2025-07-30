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
# Install minikube
echo "Installing minikube for ${ARCH}..."

MINIKUBE_VERSION=$(curl -s https://api.github.com/repos/kubernetes/minikube/releases/latest | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')

# Download minikube for detected architecture
download_and_install_binary "${PROJECT_DIR:-.}/bin" minikube "https://github.com/kubernetes/minikube/releases/download/${MINIKUBE_VERSION}/minikube-linux-${MINIKUBE_ARCH}"

echo "Minikube ${MINIKUBE_VERSION} installed successfully for ${ARCH}"
