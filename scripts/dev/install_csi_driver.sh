#!/usr/bin/env bash

set -eux

source scripts/funcs/kubernetes

# Path to the deploy script
DEPLOY_SCRIPT_PATH="./deploy/kubernetes-latest/deploy.sh"
EXTRACTED_DIR="csi-driver-host-path-1.14.1"

csi_driver_download() {
    echo "install resizable csi"
    # Define variables
    REPO_URL="https://github.com/kubernetes-csi/csi-driver-host-path/archive/refs/tags/v1.14.1.tar.gz"
    TAR_FILE="csi-driver-host-path-v1.14.1.tar.gz"

    # Download the tar.gz file
    echo "Downloading ${REPO_URL}..."
    curl -L -o "${TAR_FILE}" "${REPO_URL}"

    # Extract the tar.gz file
    echo "Extracting ${TAR_FILE}..."
    tar -xzf "${TAR_FILE}"
}

# Function to deploy to a single cluster
csi_driver_deploy() {
    local context="$1"

    # Navigate to the extracted directory
    cd "${EXTRACTED_DIR}"
    # Change to the latest supported snapshotter release branch
    SNAPSHOTTER_BRANCH=release-6.3

    # Apply VolumeSnapshot CRDs
    kubectl apply --context "${context}" -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/${SNAPSHOTTER_BRANCH}/client/config/crd/snapshot.storage.k8s.io_volumesnapshotclasses.yaml
    kubectl apply --context "${context}" -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/${SNAPSHOTTER_BRANCH}/client/config/crd/snapshot.storage.k8s.io_volumesnapshotcontents.yaml
    kubectl apply --context "${context}" -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/${SNAPSHOTTER_BRANCH}/client/config/crd/snapshot.storage.k8s.io_volumesnapshots.yaml

    # Change to the latest supported snapshotter version
    SNAPSHOTTER_VERSION=v6.3.3

    # Create snapshot controller
    kubectl apply --context "${context}" -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/${SNAPSHOTTER_VERSION}/deploy/kubernetes/snapshot-controller/rbac-snapshot-controller.yaml
    kubectl apply --context "${context}" -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/${SNAPSHOTTER_VERSION}/deploy/kubernetes/snapshot-controller/setup-snapshot-controller.yaml

    # Run the deploy script with kubectl wrapped to force it to use specific context rather than rely on current context
    if ! run_script_with_wrapped_kubectl "${DEPLOY_SCRIPT_PATH}" "${context}"; then
        return 1
    fi

    echo "Installing csi storageClass"
    kubectl apply --context "${context}" -f ./examples/csi-storageclass.yaml

    echo "Deployment successful on ${context}"
    return 0
}
