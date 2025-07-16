#!/bin/bash
set -e

INIT_PID=$(pgrep -f 'tail -F -n0 /dev/null' | head -n1)
TIMEOUT_SECONDS=300  # 5 minutes timeout
WAIT_COUNT=0

# Wait for agent-launcher container to start
echo "Waiting for agent-launcher container to be ready..."
while [ ! -d "/proc/${INIT_PID}/root/opt/scripts" ]; do
  sleep 1
  WAIT_COUNT=$((WAIT_COUNT + 1))

  if [ $WAIT_COUNT -ge $TIMEOUT_SECONDS ]; then
    echo "ERROR: Timeout waiting for init container after ${TIMEOUT_SECONDS} seconds"
    echo "Init container PID: ${INIT_PID}"
    echo "Expected directory: /proc/${INIT_PID}/root/opt/scripts"
    exit 1
  fi

  if [ $((WAIT_COUNT % 30)) -eq 0 ]; then
    echo "Still waiting for init container... (${WAIT_COUNT}/${TIMEOUT_SECONDS} seconds)"
  fi
done

echo "Init container ready after ${WAIT_COUNT} seconds"

# Copy agent launcher scripts
echo "Copying agent launcher scripts..."
if ! cp -r /proc/"${INIT_PID}"/root/opt/scripts/* /opt/scripts/; then
  echo "ERROR: Failed to copy agent launcher scripts"
  exit 1
fi

# Copy probe scripts from the init-database container for readiness and liveness
echo "Copying probe scripts..."
if [ -f "/proc/${INIT_PID}/root/probes/readinessprobe" ]; then
  if cp /proc/"${INIT_PID}"/root/probes/readinessprobe /opt/scripts/readinessprobe; then
    echo "Replaced dummy readinessprobe with real one"
  else
    echo "ERROR: Failed to copy readinessprobe"
    exit 1
  fi
else
  echo "ERROR: readinessprobe not found in init container at /proc/${INIT_PID}/root/probes/readinessprobe"
  exit 1
fi

if [ -f "/proc/${INIT_PID}/root/probes/probe.sh" ]; then
  if cp /proc/"${INIT_PID}"/root/probes/probe.sh /opt/scripts/probe.sh; then
    echo "Replaced dummy probe.sh with real one (liveness probe)"
  else
    echo "ERROR: Failed to copy probe.sh"
    exit 1
  fi
else
  echo "ERROR: probe.sh not found in init container at /proc/${INIT_PID}/root/probes/probe.sh"
  exit 1
fi

# Make scripts executable
chmod +x /opt/scripts/*.sh /opt/scripts/readinessprobe

# Start the agent launcher
echo "Starting agent launcher..."
exec /opt/scripts/agent-launcher.sh
