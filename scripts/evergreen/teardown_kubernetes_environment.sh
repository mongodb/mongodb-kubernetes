#!/usr/bin/env bash

set -Eeou pipefail

source scripts/dev/set_env_context.sh

if [ "${KUBE_ENVIRONMENT_NAME}" = "minikube" ]; then
    # Record the minikube container's netns inode before `minikube delete`;
    # leaked netns-* names are random, so we match by inode only.
    # Requires sudo, so only captured when passwordless sudo is available.
    minikube_netns_inode=""
    if sudo -n true 2>/dev/null; then
        minikube_pid="$(sudo podman inspect -f '{{.State.Pid}}' minikube 2>/dev/null || true)"
        if [ -n "${minikube_pid}" ] && [ "${minikube_pid}" != "0" ]; then
            minikube_netns_inode="$(sudo stat -c %i "/proc/${minikube_pid}/ns/net" 2>/dev/null || true)"
        fi
    else
        echo "WARNING: unable to run sudo; skipping Podman Minikube leftover cleanup"
    fi

    # Best-effort cluster deletion; never block the rest of the cleanup.
    echo "Deleting minikube cluster"
    sudo TMPDIR="/root/.minikube-tmp" "${PROJECT_DIR:-.}/bin/minikube" delete || true

    if sudo -n true 2>/dev/null; then
        sudo podman rm -f minikube 2>/dev/null || true

        # Kill processes in the leaked netns only if its inode matches the
        # minikube container's; skip if the container is already gone.
        if [ -n "${minikube_netns_inode}" ]; then
            for netns in $(sudo ip netns list 2>/dev/null | awk '{print $1}'); do
                netns_inode="$(sudo stat -c %i "/var/run/netns/${netns}" 2>/dev/null || true)"
                if [ "${netns_inode}" = "${minikube_netns_inode}" ]; then
                    echo "Killing leaked Minikube network namespace: ${netns}"
                    sudo ip netns pids "${netns}" 2>/dev/null | xargs -r sudo kill 2>/dev/null || true
                    sudo ip netns del "${netns}" 2>/dev/null || true
                fi
            done
        else
            echo "WARNING: could not identify the Minikube container's network namespace (container may already be removed); skipping netns cleanup"
        fi

        # Delete an orphaned podman1 bridge only if it carries the minikube subnet.
        if sudo ip addr show dev podman1 2>/dev/null | grep -q "192\.168\.49\.1/24"; then
            echo "Deleting orphaned podman1 bridge for minikube subnet"
            sudo ip link del podman1 2>/dev/null || true
        fi
    else
        echo "WARNING: unable to run sudo; skipping netns and bridge cleanup"
    fi
else
    kind delete clusters --all 2>/dev/null || true
    docker system prune --all --volumes --force
fi
