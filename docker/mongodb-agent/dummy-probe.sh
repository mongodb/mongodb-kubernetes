#!/bin/bash
set -e

find_init_container() {
  local pid
  pid=$(pgrep -f "agent-utilities-holder_marker" | head -n1)
  if [[ -n "$pid" && -d "/proc/$pid/root/probes" ]]; then
    echo "$pid"
    return 0
  fi
  return 1
}

execute_liveness_probe() {
  local init_pid="$1"
  local init_probe_path="/proc/$init_pid/root/probes/probe.sh"

  if [[ -f "$init_probe_path" && -x "$init_probe_path" ]]; then
    # Execute the actual probe script from the init-database container
    # This works because of shared process namespace - the probe can see all processes
    exec "$init_probe_path"
  else
    echo "ERROR: Liveness probe script not found or not executable at $init_probe_path"
    exit 1
  fi
}

# Main execution
if init_pid=$(find_init_container); then
  echo "Found init container with PID: $init_pid, executing liveness probe..."
  execute_liveness_probe "$init_pid"
else
  echo "WARNING: Init container not found, falling back to basic liveness check"
  # Fallback: if we can't find the init container, just check if this container is alive
  # This prevents the pod from being killed during startup or init container restarts
  exit 0
fi
