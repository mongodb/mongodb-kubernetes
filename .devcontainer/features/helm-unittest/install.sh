#!/usr/bin/env bash
# Devcontainer feature: helm-unittest
#
# Installs the helm-unittest plugin into the container image so it is available
# without requiring a separate on-create step.
# Requires helm to already be on PATH (provided by the kubectl-helm-minikube feature).

set -euo pipefail

REMOTE_USER="${_REMOTE_USER:-vscode}"

echo "Installing helm-unittest plugin for user '${REMOTE_USER}'..."
su - "${REMOTE_USER}" -c 'helm plugin install https://github.com/helm-unittest/helm-unittest.git --verify=false'
