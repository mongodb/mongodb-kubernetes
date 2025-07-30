#!/usr/bin/env bash
set -Eeou pipefail

source scripts/dev/set_env_context.sh

echo "Installing Docker"

# Detect OS
if [[ -f /etc/redhat-release ]]; then
    OS_TYPE="rhel"
elif [[ -f /etc/debian_version ]]; then
    OS_TYPE="debian"
else
    echo "Unsupported OS. This script supports RHEL/CentOS and Ubuntu/Debian."
    exit 1
fi

# Install Docker based on OS
if [[ "$OS_TYPE" == "rhel" ]]; then
    echo "Detected RHEL/CentOS system..."

    # Add Docker repository
    sudo yum-config-manager --add-repo https://download.docker.com/linux/centos/docker-ce.repo

    # Install Docker CE
    sudo yum install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin

elif [[ "$OS_TYPE" == "debian" ]]; then
    echo "Detected Ubuntu/Debian system..."

    # Update package index
    sudo apt-get update

    # Install required packages
    sudo apt-get install -y apt-transport-https ca-certificates curl gnupg lsb-release

    # Add Docker's official GPG key
    curl -fsSL https://download.docker.com/linux/ubuntu/gpg | sudo gpg --dearmor -o /usr/share/keyrings/docker-archive-keyring.gpg

    # Set up stable repository
    echo "deb [arch=s390x signed-by=/usr/share/keyrings/docker-archive-keyring.gpg] https://download.docker.com/linux/ubuntu $(lsb_release -cs) stable" | sudo tee /etc/apt/sources.list.d/docker.list > /dev/null

    # Update package index again
    sudo apt-get update

    # Install Docker CE
    sudo apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
fi

# Start and enable Docker service
sudo systemctl start docker
sudo systemctl enable docker

# Add current CI user to docker group and change socket permissions
current_user=$(whoami)
if [[ "$current_user" != "root" ]]; then
    echo "Adding CI user '$current_user' to docker group..."
    sudo usermod -aG docker "$current_user"
    
    # For CI: Change docker socket permissions to allow immediate access
    echo "Setting docker socket permissions for CI..."
    sudo chmod 666 /var/run/docker.sock
fi

# Verify installation
echo "Verifying Docker installation..."
sudo docker --version

# Test docker access
echo "Testing Docker access..."
if docker ps >/dev/null 2>&1; then
  echo "✅ Docker access confirmed"
else
  echo "❌ Docker access failed - CI may not work properly"
  echo "Trying with sudo..."
  if sudo docker ps >/dev/null 2>&1; then
    echo "⚠️  Docker only works with sudo"
  fi
fi
