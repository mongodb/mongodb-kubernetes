#!/usr/bin/env bash
set -Eeou pipefail

# Script to install Docker on s390x architecture (specifically for RHEL/Ubuntu based systems)

print_usage() {
    echo "Usage: $0 [options]"
    echo "Options:"
    echo "  -h, --help    Show this help message"
    echo "  -u, --user    Username to add to docker group (optional)"
    echo ""
    echo "This script installs Docker on s390x architecture systems."
}

DOCKER_USER=""

while [[ $# -gt 0 ]]; do
    case $1 in
        -h|--help)
            print_usage
            exit 0
            ;;
        -u|--user)
            DOCKER_USER="$2"
            shift 2
            ;;
        *)
            echo "Unknown option: $1"
            print_usage
            exit 1
            ;;
    esac
done

echo "Installing Docker on s390x architecture..."

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
    
    # Remove any existing Docker packages
    sudo yum remove -y docker docker-client docker-client-latest docker-common docker-latest docker-latest-logrotate docker-logrotate docker-engine || true
    
    # Install required packages (some may not exist on newer RHEL versions)
    sudo yum install -y yum-utils || echo "yum-utils already installed or unavailable"
    sudo yum install -y device-mapper-persistent-data lvm2 || echo "device-mapper packages may not be available on this system"
    
    # Add Docker repository
    sudo yum-config-manager --add-repo https://download.docker.com/linux/centos/docker-ce.repo
    
    # Install Docker CE
    sudo yum install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
    
elif [[ "$OS_TYPE" == "debian" ]]; then
    echo "Detected Ubuntu/Debian system..."
    
    # Remove any existing Docker packages
    sudo apt-get remove -y docker docker-engine docker.io containerd runc || true
    
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

# Add user to docker group if specified
if [[ -n "$DOCKER_USER" ]]; then
    echo "Adding user '$DOCKER_USER' to docker group..."
    sudo usermod -aG docker "$DOCKER_USER"
    echo "Note: User '$DOCKER_USER' needs to log out and log back in for group membership to take effect."
fi

# Verify installation
echo "Verifying Docker installation..."
sudo docker --version
sudo docker run --rm hello-world

echo "Docker installation completed successfully!"
echo ""
echo "If you added a user to the docker group, they need to log out and log back in."
echo "You can also run 'newgrp docker' to apply the group membership in the current session."