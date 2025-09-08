#!/usr/bin/env bash

set -Eeou pipefail

echo "Installing/upgrading crun..."
sudo dnf upgrade -y crun || sudo dnf install -y crun || sudo yum upgrade -y crun || sudo yum install -y crun

if ! crun --version &>/dev/null; then
  echo "❌ crun installation failed"
  exit 1
fi

current_version=$(crun --version | head -n1)
echo "✅ Using crun: ${current_version}"

# Clean up any existing conflicting configurations
echo "Cleaning up existing container configurations..."
rm -f ~/.config/containers/containers.conf 2>/dev/null || true
sudo rm -f /root/.config/containers/containers.conf 2>/dev/null || true
sudo rm -f /etc/containers/containers.conf 2>/dev/null || true

crun_path=$(which crun)
echo "Using crun path: ${crun_path}"

config="[containers]
cgroup_manager = \"cgroupfs\"

[engine]
runtime = \"crun\""

mkdir -p ~/.config/containers
echo "${config}" > ~/.config/containers/containers.conf

sudo mkdir -p /root/.config/containers
echo "${config}" | sudo tee /root/.config/containers/containers.conf >/dev/null

echo "✅ Configured crun"
