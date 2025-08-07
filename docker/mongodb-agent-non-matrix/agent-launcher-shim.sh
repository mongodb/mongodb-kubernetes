#!/bin/bash
set -e

SCRIPTS_DIR="/opt/scripts"

# Function to start the agent launcher
start_agent_launcher() {
  echo "Starting agent launcher..."
  echo "Final contents of $SCRIPTS_DIR:"
  ls -la "$SCRIPTS_DIR"

  if [[ -f "$SCRIPTS_DIR/agent-launcher.sh" ]]; then
    echo "Found agent-launcher.sh, executing..."
    exec "$SCRIPTS_DIR/agent-launcher.sh"
  else
    echo "ERROR: agent-launcher.sh not found"
    exit 1
  fi
}

main() {
  echo "Running setup-agent-files.sh..."
  if ! /usr/local/bin/setup-agent-files.sh; then
    echo "ERROR: Failed to set up agent files"
    exit 1
  fi

  start_agent_launcher
}

main "$@"
