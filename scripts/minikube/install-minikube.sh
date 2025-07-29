#!/usr/bin/env bash
set -Eeou pipefail

# Script to install and configure minikube on s390x architecture

print_usage() {
    echo "Usage: $0 [options]"
    echo "Options:"
    echo "  -h, --help              Show this help message"
    echo "  -v, --version VERSION   Minikube version to install (default: latest)"
    echo "  -k, --kubernetes VER    Kubernetes version (default: latest stable)"
    echo "  -m, --memory MEMORY     Memory allocation (default: 8192mb)"
    echo "  -c, --cpus CPUS         CPU allocation (default: 4)"
    echo "  --profile PROFILE       Minikube profile name (default: minikube)"
    echo "  --start                 Start minikube after installation"
    echo ""
    echo "This script installs minikube on s390x architecture and configures it for MongoDB Kubernetes e2e testing."
}

MINIKUBE_VERSION="latest"
K8S_VERSION=""
MEMORY="8192"
CPUS="4"
PROFILE="minikube"
START_MINIKUBE="false"

while [[ $# -gt 0 ]]; do
    case $1 in
        -h|--help)
            print_usage
            exit 0
            ;;
        -v|--version)
            MINIKUBE_VERSION="$2"
            shift 2
            ;;
        -k|--kubernetes)
            K8S_VERSION="$2"
            shift 2
            ;;
        -m|--memory)
            MEMORY="$2"
            shift 2
            ;;
        -c|--cpus)
            CPUS="$2"
            shift 2
            ;;
        --profile)
            PROFILE="$2"
            shift 2
            ;;
        --start)
            START_MINIKUBE="true"
            shift
            ;;
        *)
            echo "Unknown option: $1"
            print_usage
            exit 1
            ;;
    esac
done

echo "Installing minikube on s390x architecture..."

# Verify Docker is installed
if ! command -v docker &> /dev/null; then
    echo "Error: Docker is required but not installed. Please install Docker first."
    echo "You can use the install-docker.sh script in this directory."
    exit 1
fi

# Verify Docker is running
if ! docker info &> /dev/null; then
    echo "Error: Docker is not running. Please start Docker service."
    exit 1
fi

# Install kubectl if not present
if ! command -v kubectl &> /dev/null; then
    echo "Installing kubectl..."
    
    # Get the latest kubectl version if not specified
    if [[ -z "$K8S_VERSION" ]]; then
        K8S_VERSION=$(curl -L -s https://dl.k8s.io/release/stable.txt)
    fi
    
    # Download kubectl for s390x
    curl -LO "https://dl.k8s.io/release/${K8S_VERSION}/bin/linux/s390x/kubectl"
    
    # Verify the binary
    curl -LO "https://dl.k8s.io/${K8S_VERSION}/bin/linux/s390x/kubectl.sha256"
    echo "$(cat kubectl.sha256)  kubectl" | sha256sum --check
    
    # Install kubectl
    sudo install -o root -g root -m 0755 kubectl /usr/local/bin/kubectl
    rm -f kubectl kubectl.sha256
    
    echo "kubectl installed successfully"
fi

# Install minikube
echo "Installing minikube..."

if [[ "$MINIKUBE_VERSION" == "latest" ]]; then
    # Get the latest minikube version
    MINIKUBE_VERSION=$(curl -s https://api.github.com/repos/kubernetes/minikube/releases/latest | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')
fi

# Download minikube for s390x
curl -Lo minikube "https://github.com/kubernetes/minikube/releases/download/${MINIKUBE_VERSION}/minikube-linux-s390x"

# Make it executable and install
chmod +x minikube
sudo install minikube /usr/local/bin/

# Clean up
rm -f minikube

echo "Minikube ${MINIKUBE_VERSION} installed successfully"

# Configure minikube for MongoDB Kubernetes testing
echo "Configuring minikube for MongoDB Kubernetes e2e testing..."

# Set default driver to docker
minikube config set driver docker

# Configure resource limits
minikube config set memory "${MEMORY}mb"
minikube config set cpus "${CPUS}"

# Enable required addons for testing
ADDONS=(
    "storage-provisioner"
    "default-storageclass"
    "volumesnapshots"
    "csi-hostpath-driver"
)

echo "Minikube configuration completed."

if [[ "$START_MINIKUBE" == "true" ]]; then
    echo "Starting minikube cluster with profile '${PROFILE}'..."
    
    # Start minikube with specific configuration for MongoDB testing
    minikube start \
        --profile="${PROFILE}" \
        --driver=docker \
        --memory="${MEMORY}mb" \
        --cpus="${CPUS}" \
        --disk-size=50g \
        --extra-config=kubelet.authentication-token-webhook=true \
        --extra-config=kubelet.authorization-mode=Webhook \
        --extra-config=scheduler.bind-address=0.0.0.0 \
        --extra-config=controller-manager.bind-address=0.0.0.0 \
        ${K8S_VERSION:+--kubernetes-version=$K8S_VERSION}
    
    # Wait for cluster to be ready
    echo "Waiting for cluster to be ready..."
    kubectl wait --for=condition=Ready nodes --all --timeout=300s
    
    # Enable addons
    for addon in "${ADDONS[@]}"; do
        echo "Enabling addon: $addon"
        minikube addons enable "$addon" --profile="${PROFILE}" || true
    done
    
    # Create directories that MongoDB tests expect (similar to kind setup)
    echo "Setting up test directories..."
    minikube ssh --profile="${PROFILE}" -- 'sudo mkdir -p /opt/data/mongo-data-{0..2} /opt/data/mongo-logs-{0..2}'
    minikube ssh --profile="${PROFILE}" -- 'sudo chmod 777 /opt/data/mongo-data-* /opt/data/mongo-logs-*'
    
    echo "Minikube cluster started successfully!"
    echo ""
    echo "To use this cluster:"
    echo "  export KUBECONFIG=\$(minikube kubeconfig --profile=${PROFILE})"
    echo "  kubectl get nodes"
    echo ""
    echo "To stop the cluster:"
    echo "  minikube stop --profile=${PROFILE}"
else
    echo ""
    echo "Minikube installed but not started."
    echo "To start minikube later, run:"
    echo "  minikube start --profile=${PROFILE} --driver=docker --memory=${MEMORY}mb --cpus=${CPUS}"
fi

echo ""
echo "Installation completed successfully!"