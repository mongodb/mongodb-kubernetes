#!/usr/bin/env bash
# Devcontainer feature: helm-unittest
#
# Installs the helm-unittest plugin into the container image so it is available
# without requiring a separate on-create step.
# Requires helm to already be on PATH (provided by the kubectl-helm-minikube feature).
#
# We clone a pinned tag and install from the local checkout because the
# install-binary.sh hook in newer releases (>=1.1.0) fetches a .sha256.txt file
# that doesn't exist for linux-arm64, breaking image builds on Apple Silicon.

set -euo pipefail

REMOTE_USER="${_REMOTE_USER:-vscode}"
PLUGIN_VERSION="v1.0.3"
PLUGIN_DIR="/tmp/helm-unittest-${PLUGIN_VERSION}"

echo "Installing helm-unittest plugin ${PLUGIN_VERSION} for user '${REMOTE_USER}'..."
git clone --depth 1 --branch "${PLUGIN_VERSION}" \
    https://github.com/helm-unittest/helm-unittest.git \
    "${PLUGIN_DIR}"
chown -R "${REMOTE_USER}:${REMOTE_USER}" "${PLUGIN_DIR}"
su - "${REMOTE_USER}" -c "helm plugin install ${PLUGIN_DIR} --verify=false"
