#!/bin/bash
# post-start.sh - Runs inside the container after starting.

set -euo pipefail

# wt-ctl and make run in-container commands as `bash -lc` without a tty. The base
# image's ~/.bash_logout then runs clear_console, which fails without TERM, and its
# status becomes the command's. Clear the screen only for an interactive login.
cat >"${HOME}/.bash_logout" <<'EOF'
# ~/.bash_logout: executed by bash(1) when login shell exits.
if [[ "$SHLVL" = 1 && -t 0 ]]; then
    [ -x /usr/bin/clear_console ] && /usr/bin/clear_console -q
fi
EOF

# The main repo's .git is mounted into the container of a linked worktree, which sees the metadata of
# the other worktrees but not their directories. Without this an automatic `git gc` would prune them.
git config --global gc.worktreePruneExpire never

# git over ssh (origin is git@github.com:...) needs GitHub's host key, and the container has no ~/.ssh.
github_key='github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl'
install -d -m 700 "${HOME}/.ssh"
grep -qsF "${github_key}" "${HOME}/.ssh/known_hosts" || echo "${github_key}" >>"${HOME}/.ssh/known_hosts"

# The Evergreen CLI takes a lock file next to its token, so the container keeps its own copy of the host's read-only
# ~/.kanopy. The copy has no refresh token: a refresh in the container would invalidate the host's token and log the
# host out. When the access token expires, `evergreen` in the container asks for a login: do not complete it, run
# `evergreen client get-oauth-token` on the host and restart the container.
if [[ -d /host-kanopy ]]; then
  install -d -m 700 "${HOME}/.kanopy"
  cp -R /host-kanopy/. "${HOME}/.kanopy/"
  token_file="${HOME}/.kanopy/token-oidclogin.json"
  if [[ -f "${token_file}" ]]; then
    if jq 'del(.refresh_token)' "${token_file}" >"${token_file}.tmp"; then
      mv "${token_file}.tmp" "${token_file}"
    else
      rm -f "${token_file}" "${token_file}.tmp"
    fi
  fi
fi

# k8s-proxy does not keep its kubeconfig, so every restart of it drops the registration and breaks
# *.svc.cluster.local. Send the file again on every container start. Best effort: the proxy may not be up yet.
container_kubeconfig=/workspace/.generated/current.devc.kubeconfig
if [[ -s "${container_kubeconfig}" ]]; then
  curl --max-time 5 -fsS -X PATCH \
    -H 'Content-Type: application/yaml' \
    --data-binary @"${container_kubeconfig}" \
    "http://k8s-proxy:80/kubeconfig" \
    && echo "registered with in-container k8s-proxy on k8s-proxy:80" \
    || echo "in-container k8s-proxy not reachable on k8s-proxy:80; skipping registration"
else
  echo "no .generated/current.devc.kubeconfig yet; skipping in-container k8s-proxy registration"
fi

# Register the worktree's host kubeconfig with the kube-forwarding-proxy daemon of a Mac host, so that host shells
# resolve *.svc.cluster.local. The daemon forgets registrations when it restarts, so send it on every container
# start. Best effort; on a host without the daemon this does nothing.
/workspace/scripts/dev/wt-ctl --quiet kfp register \
  || echo "kfp registration failed (non-fatal): host shells do not resolve *.svc.cluster.local"

# VS Code forwards the host's ssh-agent socket and sets SSH_AUTH_SOCK; the bare `devcontainer` CLI
# does not. Without an agent the evg-host-proxy sidecar cannot reach an Evergreen host.
if [[ -z "${SSH_AUTH_SOCK:-}" ]]; then
  echo "SSH_AUTH_SOCK is not set; skipping ssh-agent fan-out"
  exit 0
fi
echo "SSH_AUTH_SOCK: ${SSH_AUTH_SOCK}"

# Docker Desktop's agent socket is root:root 0660 and the /ssh-agent volume is root-owned on first
# creation, so the non-root user can neither connect to the socket nor bind the relay in /ssh-agent.
if [[ -S "${SSH_AUTH_SOCK}" ]]; then
  sudo -n chmod 0666 "${SSH_AUTH_SOCK}" 2>/dev/null \
    || chmod 0666 "${SSH_AUTH_SOCK}" 2>/dev/null \
    || echo "post-start: warn: could not chmod ${SSH_AUTH_SOCK}; ssh-agent fan-out may fail"
fi
sudo -n chown "$(id -u):$(id -g)" /ssh-agent 2>/dev/null \
  || echo "post-start: warn: could not chown /ssh-agent; ssh-agent relay may fail"

# postStartCommand runs on every container start and the relay is a long-lived screen session, so
# quit a live relay by name before starting a new one. `screen -ls` exits 1 when there is none.
{ screen -ls 2>/dev/null || true; } | awk '/[0-9]+\.ssh-agent-proxy\b/ {print $1}' | while read -r s; do
  screen -S "${s}" -X quit 2>/dev/null || true
done
pkill -f 'UNIX-LISTEN:/ssh-agent/socket' 2>/dev/null || true
screen -wipe >/dev/null 2>&1 || true
rm -f /ssh-agent/socket

# The loop restarts socat when the host agent forwarding drops (Docker restart, sleep and wake).
# SSH_AUTH_SOCK is expanded here on purpose, the rest runs in screen's bash.
# shellcheck disable=SC2016
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
