#!/bin/bash
set -e

SCRIPTS_DIR="/opt/scripts"

# Set up dummy probe scripts
# liveness always returns success
# readiness always returns failure
setup_dummy_probes() {
  echo "Setting up dummy probe scripts..."
  cp /usr/local/bin/dummy-probe.sh "$SCRIPTS_DIR/probe.sh"
  cp /usr/local/bin/dummy-readinessprobe "$SCRIPTS_DIR/readinessprobe"
  echo "Dummy probe scripts ready"
}

# wait max 5m
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
    if [[ ! -f "$init_scripts_dir/$script" ]]; then
      echo "ERROR: $script not found in init container"
      return 1
    fi
    ln -sf "$init_scripts_dir/$script" "$SCRIPTS_DIR/$script"
    echo "Linked $script"
  done
}

# Link probe scripts from init container and replacing dummy ones
link_probe_scripts() {
  local init_probes_dir="$1"

  echo "Linking probe scripts..."
  for probe in probe.sh readinessprobe; do
    if [[ -f "$init_probes_dir/$probe" ]]; then
      ln -sf "$init_probes_dir/$probe" "$SCRIPTS_DIR/$probe"
      echo "Replaced dummy $probe with real one"
    else
      echo "WARNING: $probe not found in init container"
      exit 1
    fi
  done
}

start_agent() {
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

    # Verify scripts directory exists
    if [[ ! -d "$init_probes" ]]; then
      echo "ERROR: Scripts directory $init_probes not found"
      exit 1
    fi

    # Link scripts from init container
    link_agent_scripts "$init_scripts"
    link_probe_scripts "$init_probes"
  else
    echo "No init container found exiting"
      exit 1
  fi

  start_agent
}

main "$@"
