#!/usr/bin/env bash

set -Eeou pipefail

echo "Setting up Docker as container runtime for IBM hosts..."

# Check if docker is available
if ! command -v docker &>/dev/null; then
  echo "❌ Docker is not installed. Please install Docker first."
  exit 1
fi

# Check if docker daemon is running
if ! docker info &>/dev/null; then
  echo "Docker daemon not running, attempting to start..."
  sudo systemctl start docker || {
    echo "❌ Failed to start Docker daemon"
    exit 1
  }
fi

docker_version=$(docker --version)
echo "✅ Using Docker: ${docker_version}"

# Ensure current user can run docker without sudo
if ! docker ps &>/dev/null 2>&1; then
  echo "Adding current user to docker group..."
  sudo usermod -aG docker "${USER}" || true
  echo "Note: You may need to log out and back in for group changes to take effect"
fi

echo "✅ Docker container runtime configured"
