#!/bin/bash
set -e

# Configuration variables
TIMEOUT_SECONDS=300  # 5 minutes timeout
TARGET_SCRIPTS_DIR="/opt/scripts"

setup_dummy_probes() {
  echo "Setting up dummy probe scripts..."
  cp /usr/local/bin/dummy-probe.sh "${TARGET_SCRIPTS_DIR}"/probe.sh
  cp /usr/local/bin/dummy-readinessprobe "${TARGET_SCRIPTS_DIR}"/readinessprobe
  echo "Dummy probe scripts ready"
}

# Find the init container process with retries
find_init_container() {
  echo "Looking for init/utilities container process..."
  for attempt in {1..5}; do
    local pid=$(pgrep -f 'tail -f /dev/null' | head -n1)
    if [ -n "$pid" ]; then
      echo "Found init container process with PID: ${pid} (attempt ${attempt})"
      echo "$pid"
      return 0
    fi
    echo "Attempt ${attempt}: No process found, retrying in 1 second..."
    sleep 1
  done
  return 1
}

# Check if agent launcher already exists in image
check_existing_scripts() {
  if [ -f "${TARGET_SCRIPTS_DIR}/agent-launcher.sh" ]; then
    echo "Agent launcher script already exists in image"
    return 0
  else
    echo "ERROR: No init container found and no agent-launcher.sh in image"
    echo "Available processes:"
    ps aux
    return 1
  fi
}

# Wait for init container directories to be ready
wait_for_init_container() {
  local init_scripts_dir="$1"
  local init_container_root="$2"
  local init_pid="$3"
  local wait_count=0

  if [ -d "${init_scripts_dir}" ]; then
    echo "Scripts directory found immediately: ${init_scripts_dir}"
    return 0
  fi

  echo "Scripts directory not found, waiting..."
  echo "Waiting for agent-launcher container to be ready..."

  while [ ! -d "${init_scripts_dir}" ]; do
    sleep 1
    wait_count=$((wait_count + 1))

    if [ $wait_count -ge $TIMEOUT_SECONDS ]; then
      echo "ERROR: Timeout waiting for init container after ${TIMEOUT_SECONDS} seconds"
      echo "Init container PID: ${init_pid}"
      echo "Expected directory: ${init_scripts_dir}"
      echo "Available directories in init container root:"
      ls -la "${init_container_root}/" 2>/dev/null || echo "Cannot list init container root"
      echo "Process still running?"
      ps -p "${init_pid}" || echo "Process ${init_pid} no longer exists"
      return 1
    fi

    if [ $((wait_count % 30)) -eq 0 ]; then
      echo "Still waiting for init container... (${wait_count}/${TIMEOUT_SECONDS} seconds)"
      echo "Current init container root contents:"
      ls -la "${init_container_root}/" 2>/dev/null || echo "Cannot access ${init_container_root}"
    fi
  done

  echo "Init container ready after ${wait_count} seconds"
  return 0
}

# Copy agent launcher scripts from init container
copy_agent_scripts() {
  local init_scripts_dir="$1"

  echo "Copying agent launcher scripts..."
  echo "Available files in ${init_scripts_dir}:"
  ls -la "${init_scripts_dir}/" 2>/dev/null || echo "Cannot list scripts directory"

  if ! cp -r "${init_scripts_dir}"/* "${TARGET_SCRIPTS_DIR}"/; then
    echo "ERROR: Failed to copy agent launcher scripts"
    return 1
  fi

  echo "Agent launcher scripts copied successfully"
  return 0
}

# Copy probe scripts from init container
copy_probe_scripts() {
  local init_probes_dir="$1"

  echo "Copying probe scripts..."
  echo "Available files in ${init_probes_dir}:"
  ls -la "${init_probes_dir}/" 2>/dev/null || echo "Probes directory not found"

  # Copy readiness probe
  if [ -f "${init_probes_dir}/readinessprobe" ]; then
    if cp "${init_probes_dir}"/* "${TARGET_SCRIPTS_DIR}"/; then
      echo "Replaced dummy readinessprobe with real one"
    else
      echo "ERROR: Failed to copy readinessprobe"
      return 1
    fi
  else
    echo "WARNING: readinessprobe not found in init container at ${init_probes_dir}/readinessprobe"
    echo "Keeping dummy readiness probe"
  fi

  # Copy liveness probe
  if [ -f "${init_probes_dir}/probe.sh" ]; then
    if cp "${init_probes_dir}"/probe.sh "${TARGET_SCRIPTS_DIR}"/probe.sh; then
      echo "Replaced dummy probe.sh with real one (liveness probe)"
    else
      echo "ERROR: Failed to copy probe.sh"
      return 1
    fi
  else
    echo "WARNING: probe.sh not found in init container at ${init_probes_dir}/probe.sh"
    echo "Keeping dummy liveness probe"
  fi

  return 0
}

# Start the agent launcher
start_agent_launcher() {
  echo "Starting agent launcher..."
  echo "Final contents of ${TARGET_SCRIPTS_DIR}:"
  ls -la "${TARGET_SCRIPTS_DIR}/"

  if [ -f "${TARGET_SCRIPTS_DIR}/agent-launcher.sh" ]; then
    exec "${TARGET_SCRIPTS_DIR}"/agent-launcher.sh
  else
    echo "ERROR: agent-launcher.sh not found at ${TARGET_SCRIPTS_DIR}/agent-launcher.sh"
    return 1
  fi
}

# Main execution flow
main() {
  # Step 1: Setup dummy probes immediately
  setup_dummy_probes

  # Step 2: Try to find init container
  if ! init_pid=$(find_init_container); then
    echo "No init container found after 5 attempts. Checking if scripts already exist in image..."

    if ! check_existing_scripts; then
      exit 1
    fi

  else
    # Step 3: Setup paths and wait for init container
    init_container_root="/proc/${init_pid}/root"
    init_scripts_dir="${init_container_root}/scripts"
    init_probes_dir="${init_container_root}/probes"

    if ! wait_for_init_container "$init_scripts_dir" "$init_container_root" "$init_pid"; then
      exit 1
    fi

    # Step 4: Copy scripts from init container
    if ! copy_agent_scripts "$init_scripts_dir"; then
      exit 1
    fi

    if ! copy_probe_scripts "$init_probes_dir"; then
      exit 1
    fi

    # Step 5: Make scripts executable
    chmod +x "${TARGET_SCRIPTS_DIR}"/*.sh "${TARGET_SCRIPTS_DIR}"/readinessprobe 2>/dev/null || true
  fi

  # Step 6: Start the agent launcher
  start_agent_launcher
}

# Run main function
main "$@"
