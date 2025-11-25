#!/bin/bash
set -e

SCRIPTS_DIR="/opt/scripts"

# Set up dummy probe scripts
# liveness always returns success
# readiness always returns failure
setup_dummy_probes() {
  echo "Setting up dummy probe scripts..."
  cp --remove-destination /usr/local/bin/dummy-probe.sh "$SCRIPTS_DIR/probe.sh"
  cp --remove-destination /usr/local/bin/dummy-readinessprobe "$SCRIPTS_DIR/readinessprobe"
  echo "Dummy probe scripts ready"
}

# wait max 5m for init container to be ready
find_init_container() {
  for i in {1..150}; do
    local pid
    pid=$(pgrep -f "agent-utilities-holder_marker" | head -n1)
    if [[ -n "$pid" && -d "/proc/$pid/root/scripts" ]]; then
      echo "$pid"
      return 0
    fi
    echo "Waiting for init container... (attempt $i)" >&2
    sleep 2
  done
  return 1
}

link_agent_scripts() {
  local init_scripts_dir="$1"

  echo "Linking agent launcher scripts..."
  for script in agent-launcher.sh agent-launcher-lib.sh; do
    ln -sf "$init_scripts_dir/$script" "$SCRIPTS_DIR/$script"
    echo "Linked $script"
  done
}


# Main function to set up all files
main() {
  setup_dummy_probes

  if init_pid=$(find_init_container); then
    echo "Found init container with PID: $init_pid"

    init_root="/proc/$init_pid/root"
    init_scripts="$init_root/scripts"
    init_probes="$init_root/probes"

    # Verify scripts directory exists
    if [[ ! -d "$init_scripts" ]]; then
      echo "ERROR: Scripts directory $init_scripts not found"
      exit 1
    fi

    # Verify probes directory exists
    if [[ ! -d "$init_probes" ]]; then
      echo "ERROR: Probes directory $init_probes not found"
      exit 1
    fi

    # Link scripts from init container
    link_agent_scripts "$init_scripts"

    echo "File setup completed successfully"
    exit 0
  else
    echo "No init container found"
    exit 1
  fi
}

# Only run main if script is executed directly
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  main "$@"
fi
