#!/usr/bin/env bash

set -Eeou pipefail

echo "Setting up IBM container runtime (rootful podman for minikube)"

# Install crun if not present (OCI runtime for cgroup v2)
if ! command -v crun &>/dev/null; then
    echo "Installing crun..."
    sudo dnf install -y crun --disableplugin=subscription-manager 2>/dev/null || \
    sudo yum install -y crun --disableplugin=subscription-manager 2>/dev/null || \
    echo "Warning: Could not install crun"
else
    echo "crun already installed: $(crun --version | head -1)"
fi

# Configure root-level podman
sudo mkdir -p /etc/containers
sudo tee /etc/containers/containers.conf > /dev/null << 'EOF'
[containers]
cgroup_manager = "systemd"

[engine]
runtime = "crun"
EOF

# Test sudo podman (used by minikube in rootful mode)
# Minikube setup owns cluster cleanup; do not reset unrelated host resources.
echo "Testing sudo podman..."
sudo podman run --rm docker.io/library/alpine:latest echo "sudo podman works"

echo "Container runtime setup complete"
echo "  crun: $(crun --version 2>/dev/null | head -1 || echo 'not found')"
echo "  podman: $(sudo podman --version)"
