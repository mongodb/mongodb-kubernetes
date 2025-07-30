#!/usr/bin/env bash

# Consolidated setup script for minikube host with multi-architecture support
# This script groups all the setup steps needed for IBM machines and other architectures
# Can be run on static hosts for testing and verification

source scripts/dev/set_env_context.sh
set -Eeoux pipefail

echo "=========================================="
echo "Setting up minikube host with multi-architecture support"
echo "Architecture: $(uname -m)"
echo "OS: $(uname -s)"
echo "=========================================="

# Function to run a setup step with error handling and logging
run_setup_step() {
    local step_name="$1"
    shift
    local script_command=("$@")

    echo ""
    echo ">>> Running: ${step_name}"
    echo ">>> Command: ${script_command[*]}"

    local script_path="${script_command[0]}"
    if [[ -f "${script_path}" ]]; then
        if "${script_command[@]}"; then
            echo "✅ ${step_name} completed successfully"
        else
            echo "❌ ${step_name} failed"
            exit 1
        fi
    else
        echo "❌ Script not found: ${script_path}"
        exit 1
    fi
}

# Setup Python environment (needed for AWS CLI pip installation)
run_setup_step "Python Virtual Environment" "scripts/dev/recreate_python_venv.sh" "--skip-requirements"

run_setup_step "AWS CLI Setup" "scripts/evergreen/setup_aws.sh"

run_setup_step "kubectl and helm Setup" "scripts/evergreen/setup_kubectl.sh"

run_setup_step "jq Setup" "scripts/evergreen/setup_jq.sh"

run_setup_step "IBM Host Setup" "scripts/minikube/setup_minikube_host.sh"

run_setup_step "Docker Authentication" "scripts/dev/configure_docker_auth.sh"

# Setup Kubernetes cluster after Docker is properly configured
echo ""
echo ">>> Setting up Kubernetes cluster"
echo ">>> Command: minikube start --profile=${MINIKUBE_PROFILE:-mongodb-e2e} --driver=docker --memory=8192mb --cpus=4"

# Start minikube cluster for CI
if minikube start --profile="${MINIKUBE_PROFILE:-mongodb-e2e}" --driver=docker --memory=8192mb --cpus=4; then
    echo "✅ Minikube Kubernetes Cluster completed successfully"
else
    echo "❌ Minikube Kubernetes Cluster failed"
    exit 1
fi

echo ""
echo "=========================================="
echo "✅ Minikube host setup completed successfully!"
echo "=========================================="
echo ""
echo "Installed tools summary:"
echo "- Python: $(python --version 2>/dev/null || python3 --version 2>/dev/null || echo 'Not found')"
echo "- AWS CLI: $(aws --version 2>/dev/null || echo 'Not found')"
echo "- kubectl: $(kubectl version --client 2>/dev/null || echo 'Not found')"
echo "- helm: $(helm version --short 2>/dev/null || echo 'Not found')"
echo "- jq: $(jq --version 2>/dev/null || echo 'Not found')"
echo "- Docker: $(docker --version 2>/dev/null || echo 'Not found')"
echo ""
echo "Setup complete! Host is ready for minikube operations."
