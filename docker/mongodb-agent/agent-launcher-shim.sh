#!/bin/bash
set -e

# Configuration variables
INIT_PID=$(pgrep -f 'tail -f /dev/null' | head -n1)
TIMEOUT_SECONDS=300  # 5 minutes timeout
WAIT_COUNT=0

# Path variables
INIT_CONTAINER_ROOT="/proc/${INIT_PID}/root"
INIT_SCRIPTS_DIR="${INIT_CONTAINER_ROOT}/scripts"
INIT_PROBES_DIR="${INIT_CONTAINER_ROOT}/probes"
TARGET_SCRIPTS_DIR="/opt/scripts"

# Wait for agent-launcher container to start
echo "Waiting for agent-launcher container to be ready..."
while [ ! -d "${INIT_SCRIPTS_DIR}" ]; do
  sleep 1
  WAIT_COUNT=$((WAIT_COUNT + 1))

  if [ $WAIT_COUNT -ge $TIMEOUT_SECONDS ]; then
    echo "ERROR: Timeout waiting for init container after ${TIMEOUT_SECONDS} seconds"
    echo "Init container PID: ${INIT_PID}"
    echo "Expected directory: ${INIT_SCRIPTS_DIR}"
    exit 1
  fi

  if [ $((WAIT_COUNT % 30)) -eq 0 ]; then
    echo "Still waiting for init container... (${WAIT_COUNT}/${TIMEOUT_SECONDS} seconds)"
  fi
done

echo "Init container ready after ${WAIT_COUNT} seconds"

# Copy agent launcher scripts
echo "Copying agent launcher scripts..."
if ! cp -r "${INIT_SCRIPTS_DIR}"/* "${TARGET_SCRIPTS_DIR}"/; then
  echo "ERROR: Failed to copy agent launcher scripts"
  exit 1
fi

# Copy probe scripts from the init-database container for readiness and liveness
echo "Copying probe scripts..."
if [ -f "${INIT_PROBES_DIR}/readinessprobe" ]; then
  if cp "${INIT_PROBES_DIR}"/* "${TARGET_SCRIPTS_DIR}"/; then
    echo "Replaced dummy readinessprobe with real one"
  else
    echo "ERROR: Failed to copy readinessprobe"
    exit 1
  fi
else
  echo "ERROR: readinessprobe not found in init container at ${INIT_PROBES_DIR}/readinessprobe"
  exit 1
fi

if [ -f "${INIT_PROBES_DIR}/probe.sh" ]; then
  if cp "${INIT_PROBES_DIR}"/probe.sh "${TARGET_SCRIPTS_DIR}"/probe.sh; then
    echo "Replaced dummy probe.sh with real one (liveness probe)"
  else
    echo "ERROR: Failed to copy probe.sh"
    exit 1
  fi
else
  echo "ERROR: probe.sh not found in init container at ${INIT_PROBES_DIR}/probe.sh"
  exit 1
fi

# Make scripts executable
chmod +x "${TARGET_SCRIPTS_DIR}"/*.sh "${TARGET_SCRIPTS_DIR}"/readinessprobe

# Start the agent launcher
echo "Starting agent launcher..."
exec "${TARGET_SCRIPTS_DIR}"/agent-launcher.sh
