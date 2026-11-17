#!/bin/bash
# Dynamic liveness probe that locates and executes the actual probe from init-database container

# Prints the root of the init-database container. With the mongodb-proxy the containers do not share a process
# namespace, and the operator mounts the init-database image at MDB_INIT_ROOT instead.
find_init_root() {
  if [[ -n "${MDB_INIT_ROOT:-}" && -d "${MDB_INIT_ROOT}/probes" ]]; then
    echo "${MDB_INIT_ROOT}"
    return 0
  fi

  local pid
  pid=$(pgrep -f "agent-utilities-holder_marker" | head -n1)
  if [[ -n "${pid}" && -d "/proc/${pid}/root/probes" ]]; then
    echo "/proc/${pid}/root"
    return 0
  fi
  return 1
}

execute_liveness_probe() {
  local init_root="${1}"
  local init_probe_path="${init_root}/probes/probe.sh"

  if [[ ! -f "${init_probe_path}" ]]; then
    echo "ERROR: Liveness probe script not found at ${init_probe_path}"
    exit 1
  elif [[ ! -x "${init_probe_path}" ]]; then
    echo "ERROR: Liveness probe script not executable at ${init_probe_path}"
    exit 1
  else
    # Execute the actual probe script from the init-database container
    # The probe sees all processes only when the process namespace is shared
    exec "${init_probe_path}"
  fi
}

# Main execution
if init_root=$(find_init_root); then
  echo "Found init container root: ${init_root}, executing liveness probe..."
  execute_liveness_probe "${init_root}"
else
  echo "WARNING: Init container not found, falling back to basic liveness check"
  # Fallback: if we can't find the init container, just check if this container is alive
  # This prevents the pod from being killed during startup or init container restarts
  exit 0
fi
