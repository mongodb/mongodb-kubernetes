#!/bin/bash
# post-start.sh - Runs inside the container after starting.

set -euo pipefail

# In-container k8s-proxy doesn't persist its registered kubeconfig, so
# every restart (force-recreate from a compose.user.yml override
# reconcile, OOM, manual `docker compose restart`) drops it and silently
# breaks cluster.local DNS. Re-PATCH on every container start so the
# source-of-truth lives in /workspace/.generated/ (bind-mounted) rather
# than the proxy's volatile memory. Best-effort, non-fatal: if the proxy
# isn't reachable yet (e.g. the container is mid-boot) we skip.
container_kubeconfig=/workspace/.generated/evg-host.devc.kubeconfig
if [[ -s "${container_kubeconfig}" ]]; then
  curl --max-time 5 -fsS -X PATCH \
    -H 'Content-Type: application/yaml' \
    --data-binary @"${container_kubeconfig}" \
    "http://k8s-proxy:80/kubeconfig" \
    && echo "registered with in-container k8s-proxy on k8s-proxy:80" \
    || echo "in-container k8s-proxy not reachable on k8s-proxy:80; skipping registration"
else
  echo "no .generated/evg-host.devc.kubeconfig yet; skipping in-container k8s-proxy registration"
fi

# VS Code automatically forwards the host's ssh-agent socket and sets
# SSH_AUTH_SOCK; the bare `devcontainer` CLI does not. If the var is unset we
# simply skip the fan-out — the autossh sidecar will fail until the agent is
# mounted explicitly via compose.user.yml, but the rest of the stack is fine.
if [[ -z "${SSH_AUTH_SOCK:-}" ]]; then
    echo "SSH_AUTH_SOCK is not set; skipping ssh-agent fan-out"
    exit 0
fi

echo "SSH_AUTH_SOCK: ${SSH_AUTH_SOCK}"

# The mounted host ssh-agent socket (Docker Desktop's
# /run/host-services/ssh-auth.sock on macOS) is owned root:root mode 0660,
# so the non-root user we run as (vscode) can't connect through it. socat
# silently fails its upstream connect with "Permission denied" — the listen
# socket /ssh-agent/socket accepts client connections but every forward
# returns an error, autossh sees publickey-denied for every restart, and
# kubectl through gost-proxy returns "Service Unavailable". chmod up the
# mounted socket so the socat process can speak to the host agent.
# Best-effort: the mount may be read-only on some hosts, so we don't fail
# if it doesn't take.
if [[ -S "${SSH_AUTH_SOCK}" ]]; then
    sudo -n chmod 0666 "${SSH_AUTH_SOCK}" 2>/dev/null \
        || chmod 0666 "${SSH_AUTH_SOCK}" 2>/dev/null \
        || echo "post-start: warn: could not chmod ${SSH_AUTH_SOCK}; ssh-agent fan-out may fail"
fi

# Remove any stale socket from a previous run before socat tries to bind it
# (socat refuses to bind UNIX-LISTEN if the path already exists). Also wipe
# any dead screen sessions left over from a previous container start so the
# session name is free.
rm -f /ssh-agent/socket
screen -wipe >/dev/null 2>&1 || true

# Wrap socat in a respawn loop: if the host's ssh-agent forwarding hiccups
# (Docker Desktop restart, system sleep/wake, agent rotation) socat exits.
# Without the loop the autossh sidecar would poll forever waiting for the
# socket to come back, since post-start.sh only fires on devcontainer start.
# SSH_AUTH_SOCK is expanded here on purpose so the value gets baked into the
# command run inside the screen subshell. Everything else stays literal.
# shellcheck disable=SC2016  # The $(...) / ${...} inside the body run in screen's bash, not here.
screen -dmS ssh-agent-proxy bash -c '
    while true; do
        rm -f /ssh-agent/socket
        socat -d UNIX-LISTEN:/ssh-agent/socket,fork,mode=777 \
              UNIX-CONNECT:'"${SSH_AUTH_SOCK}"' \
            >> /tmp/socat-ssh-agent.log 2>&1
        echo "[$(date -Is)] socat exited; restarting in 1s" \
            >> /tmp/socat-ssh-agent.log
        sleep 1
    done
'

# Best-effort registration with the host kube-forwarding-proxy. If the host
# kfp isn't running (or the host-variant kubeconfig hasn't been generated
# yet) we silently skip — this hook is non-fatal by design. The bare
# evg-host.kubeconfig IS the host variant (proxy-url 127.0.0.1:80${PREFIX});
# the .devc.kubeconfig sibling is for in-container use.
host_kubeconfig=/workspace/.generated/evg-host.kubeconfig
if [[ -s "${host_kubeconfig}" ]]; then
  curl --max-time 2 -fsS -X PATCH \
    -H 'Content-Type: application/yaml' \
    --data-binary @"${host_kubeconfig}" \
    "http://host.docker.internal:11616/kubeconfig" \
    && echo "registered with host kfp on 127.0.0.1:11616" \
    || echo "host kfp not reachable on 127.0.0.1:11616; skipping registration"
else
  echo "no .generated/evg-host.kubeconfig yet; skipping host kfp registration"
fi
