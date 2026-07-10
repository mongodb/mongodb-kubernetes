#!/bin/bash
# Install the Evergreen CLI inside the container. Done at on-create time (not
# in the Dockerfile) so the image stays stable across evergreen version bumps
# and can be pre-built once and reused across worktrees.
#
# EVERGREEN_CLI_URL is resolved on the host by initialize/evergreen-cli.sh and
# injected as an environment variable on the devcontainer service.

set -euo pipefail

if [ -z "${EVERGREEN_CLI_URL:-}" ]; then
    echo "EVERGREEN_CLI_URL not set; skipping evergreen CLI install."
    echo "(Re-open the devcontainer with the host evergreen binary on PATH to"
    echo " auto-resolve, or install manually with:"
    echo "    sudo curl -L <url> -o /usr/local/bin/evergreen && sudo chmod +x /usr/local/bin/evergreen)"
    exit 0
fi

echo "Installing evergreen CLI from ${EVERGREEN_CLI_URL}"
sudo curl -fsSL "${EVERGREEN_CLI_URL}" -o /usr/local/bin/evergreen
sudo chmod +x /usr/local/bin/evergreen
echo "Installed: $(/usr/local/bin/evergreen --version)"
