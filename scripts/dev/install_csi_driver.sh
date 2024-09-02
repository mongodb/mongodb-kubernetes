#!/bin/bash

set -eux

# Path to the deploy script
DEPLOY_SCRIPT_PATH="./deploy/kubernetes-latest/deploy.sh"

# Function to deploy to a single cluster
deploy_to_cluster() {
    local context="$1"
    echo "Switching to context: $context"
    
    # Set the current context to the target cluster
    kubectl config use-context "$context"
    if [ $? -ne 0 ]; then
        echo "Failed to switch to context: $context"
    fi

    echo "install resizable csi"
    # Define variables
    REPO_URL="https://github.com/kubernetes-csi/csi-driver-host-path/archive/refs/tags/v1.14.1.tar.gz"
    TAR_FILE="csi-driver-host-path-v1.14.1.tar.gz"
    EXTRACTED_DIR="csi-driver-host-path-1.14.1"

    # Download the tar.gz file
    echo "Downloading $REPO_URL..."
    curl -L -o "$TAR_FILE" "$REPO_URL"

    # Extract the tar.gz file
    echo "Extracting $TAR_FILE..."
    tar -xzf "$TAR_FILE"

    # Navigate to the extracted directory
    cd "$EXTRACTED_DIR"
    
    # Change to the latest supported snapshotter release branch
    SNAPSHOTTER_BRANCH=release-6.3
    
    # Apply VolumeSnapshot CRDs
    kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/${SNAPSHOTTER_BRANCH}/client/config/crd/snapshot.storage.k8s.io_volumesnapshotclasses.yaml
    kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/${SNAPSHOTTER_BRANCH}/client/config/crd/snapshot.storage.k8s.io_volumesnapshotcontents.yaml
    kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/${SNAPSHOTTER_BRANCH}/client/config/crd/snapshot.storage.k8s.io_volumesnapshots.yaml
    
    # Change to the latest supported snapshotter version
    SNAPSHOTTER_VERSION=v6.3.3
    
    # Create snapshot controller
    kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/${SNAPSHOTTER_VERSION}/deploy/kubernetes/snapshot-controller/rbac-snapshot-controller.yaml
    kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/${SNAPSHOTTER_VERSION}/deploy/kubernetes/snapshot-controller/setup-snapshot-controller.yaml

    # Run the deploy script
    echo "Running deploy script on $context"
    bash "$DEPLOY_SCRIPT_PATH"
    if [ $? -ne 0 ]; then
        echo "Failed to run deploy script on $context"
        return 1
    fi

    echo "Installing csi storageClass"
    kubectl apply -f ./examples/csi-storageclass.yaml

    echo "Deployment successful on $context"
    return 0
}

CLUSTER_CONTEXTS=()

# Set default values for the context variables if they are not set
CTX_CLUSTER="${CTX_CLUSTER:-}"
CTX_CLUSTER1="${CTX_CLUSTER1:-}"
CTX_CLUSTER2="${CTX_CLUSTER2:-}"
CTX_CLUSTER3="${CTX_CLUSTER3:-}"

# Add to CLUSTER_CONTEXTS only if the environment variable is set and not empty
[[ -n "$CTX_CLUSTER" ]] && CLUSTER_CONTEXTS+=("$CTX_CLUSTER")
[[ -n "$CTX_CLUSTER1" ]] && CLUSTER_CONTEXTS+=("$CTX_CLUSTER1")
[[ -n "$CTX_CLUSTER2" ]] && CLUSTER_CONTEXTS+=("$CTX_CLUSTER2")
[[ -n "$CTX_CLUSTER3" ]] && CLUSTER_CONTEXTS+=("$CTX_CLUSTER3")

# Main deployment loop
for context in "${CLUSTER_CONTEXTS[@]}"; do
    deploy_to_cluster "$context"
done

echo "Deployment completed for all clusters."
