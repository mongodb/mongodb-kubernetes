#!/usr/bin/env bash

set -Eeou pipefail

# Install/upgrade crun to latest version
echo "Installing/upgrading crun..."
sudo dnf upgrade -y crun || sudo dnf install -y crun || sudo yum upgrade -y crun || sudo yum install -y crun

# Verify crun works
if ! crun --version &>/dev/null; then
  echo "❌ crun installation failed"
  exit 1
fi

current_version=$(crun --version | head -n1)
echo "✅ Using crun: ${current_version}"

# Check if we have a reasonably recent version (1.10+)
crun_major=$(echo "${current_version}" | grep -oE '[0-9]+\.[0-9]+' | cut -d. -f1)
crun_minor=$(echo "${current_version}" | grep -oE '[0-9]+\.[0-9]+' | cut -d. -f2)

if [[ ${crun_major} -lt 1 ]] || [[ ${crun_major} -eq 1 && ${crun_minor} -lt 10 ]]; then
  echo "⚠️ Warning: crun version ${current_version} is older than recommended (1.10+)"
  echo "   This may cause compatibility issues with minikube"
fi

# Clean up any existing conflicting configurations
echo "Cleaning up existing container configurations..."
rm -f ~/.config/containers/containers.conf 2>/dev/null || true
sudo rm -f /root/.config/containers/containers.conf 2>/dev/null || true
sudo rm -f /etc/containers/containers.conf 2>/dev/null || true

# Configure containers.conf with proper crun settings
crun_path=$(which crun)
echo "Using crun path: ${crun_path}"

config="[containers]
cgroup_manager = \"cgroupfs\"

[engine]
runtime = \"crun\"

[engine.runtimes]
crun = [\"${crun_path}\", \"--systemd-cgroup=false\"]"

# User config
mkdir -p ~/.config/containers
echo "$config" > ~/.config/containers/containers.conf

# System config
sudo mkdir -p /root/.config/containers
echo "$config" | sudo tee /root/.config/containers/containers.conf >/dev/null
