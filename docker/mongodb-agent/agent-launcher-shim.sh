#!/bin/bash
set -e

# Wait for agent-launcher container to start
echo "Waiting for agent-launcher container to be ready..."
while [ ! -d "/proc/$(pgrep -f 'tail -F -n0 /dev/null' | head -n1)/root/opt/scripts" ]; do
  sleep 1
done

# Copy agent launcher scripts
echo "Copying agent launcher scripts..."
cp -r /proc/$(pgrep -f 'tail -F -n0 /dev/null' | head -n1)/root/opt/scripts/* /opt/scripts/

# Make scripts executable
chmod +x /opt/scripts/*.sh

# Start the agent launcher
echo "Starting agent launcher..."
exec /opt/scripts/agent-launcher.sh
