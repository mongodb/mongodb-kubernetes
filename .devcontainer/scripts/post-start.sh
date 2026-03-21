#!/bin/bash
# post-start.sh - Runs inside the container after starting.

set -euo pipefail

echo "SSH_AUTH_SOCK: ${SSH_AUTH_SOCK}"

screen -dmS ssh-agent-proxy bash -c \
  'socat -d UNIX-LISTEN:/ssh-agent/socket,fork,mode=777 UNIX-CONNECT:'"${SSH_AUTH_SOCK}"' > /tmp/socat-ssh-agent.log 2>&1'
