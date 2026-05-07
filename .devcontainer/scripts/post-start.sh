#!/bin/bash
# post-start.sh - Runs inside the container after starting.

set -euo pipefail

# VS Code automatically forwards the host's ssh-agent socket and sets
# SSH_AUTH_SOCK; the bare `devcontainer` CLI does not. If the var is unset we
# simply skip the fan-out — the autossh sidecar will fail until the agent is
# mounted explicitly via compose.user.yml, but the rest of the stack is fine.
if [[ -z "${SSH_AUTH_SOCK:-}" ]]; then
    echo "SSH_AUTH_SOCK is not set; skipping ssh-agent fan-out"
    exit 0
fi

echo "SSH_AUTH_SOCK: ${SSH_AUTH_SOCK}"

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
