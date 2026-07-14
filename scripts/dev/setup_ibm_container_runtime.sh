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

# Clean up stale container state (safe for shared CI machines)
cleanup_stale_state() {
    echo "Cleaning up stale container state..."

    # Skip if minikube is running
    if command -v minikube &>/dev/null && minikube status &>/dev/null 2>&1; then
        echo "  Minikube running - skipping cleanup"
        return 0
    fi

    # Kill orphaned root conmon processes (PPID=1 means orphaned)
    for pid in $(sudo pgrep conmon 2>/dev/null); do
        ppid=$(ps -o ppid= -p "${pid}" 2>/dev/null | tr -d ' ')
        if [[ "${ppid}" == "1" ]]; then
            echo "  Killing orphaned conmon ${pid}"
            sudo kill -9 "${pid}" 2>/dev/null || true
        fi
    done

    # Clean stale lock files (safe since minikube isn't running)
    sudo find /run/crun -name "*.lock" -delete 2>/dev/null || true

    # Prune exited containers and dangling volumes
    sudo podman container prune -f 2>/dev/null || true
    sudo podman volume prune -f 2>/dev/null || true
}

cleanup_stale_state

# Test sudo podman (used by minikube in rootful mode)
echo "Testing sudo podman..."
if ! sudo podman run --rm docker.io/library/alpine:latest echo "sudo podman works" 2>/dev/null; then
    echo "Sudo podman not working, resetting..."
    sudo podman system reset --force 2>/dev/null || true
    sleep 1

    if sudo podman run --rm docker.io/library/alpine:latest echo "sudo podman works" 2>/dev/null; then
        echo "Sudo podman working after reset"
    else
        echo "Warning: Sudo podman still not working"
    fi
else
    echo "Sudo podman working"
fi

# Configure root-level podman
sudo mkdir -p /etc/containers
sudo tee /etc/containers/containers.conf > /dev/null << 'EOF'
[containers]
cgroup_manager = "systemd"

[engine]
runtime = "crun"
EOF

echo "Container runtime setup complete"
echo "  crun: $(crun --version 2>/dev/null | head -1 || echo 'not found')"
echo "  podman: $(sudo podman --version)"
