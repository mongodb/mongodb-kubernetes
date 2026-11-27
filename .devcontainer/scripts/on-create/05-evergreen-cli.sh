#!/bin/bash
# Install the Evergreen CLI in the container. initialize.py puts the URL of the host's build in EVERGREEN_CLI_URL.

set -euo pipefail

if [[ -z "${EVERGREEN_CLI_URL:-}" ]]; then
    echo "EVERGREEN_CLI_URL not set (no evergreen CLI on the host): not installing it."
    exit 0
fi

sudo curl -fsSL "${EVERGREEN_CLI_URL}" -o /usr/local/bin/evergreen
sudo chmod +x /usr/local/bin/evergreen
echo "Installed: $(/usr/local/bin/evergreen --version)"
