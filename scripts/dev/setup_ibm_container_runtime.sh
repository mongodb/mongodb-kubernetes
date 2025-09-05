#!/usr/bin/env bash

set -Eeou pipefail

# Install crun if not available
if ! command -v crun &>/dev/null; then
  echo "Installing crun..."
  sudo dnf install -y crun || sudo yum install -y crun
fi

# Verify crun works
if ! crun --version &>/dev/null; then
  echo "❌ crun installation failed"
  exit 1
fi

echo "✅ Using crun: $(crun --version | head -n1)"

# Configure containers.conf
crun_path=$(which crun)
config="[containers]
cgroup_manager = \"cgroupfs\"

[engine]
runtime = \"crun\"

[engine.runtimes]
crun = [\"${crun_path}\"]"

# User config
mkdir -p ~/.config/containers
echo "$config" > ~/.config/containers/containers.conf

# System config
sudo mkdir -p /root/.config/containers
echo "$config" | sudo tee /root/.config/containers/containers.conf >/dev/null

echo "✅ Configured crun"
