#!/usr/bin/env bash

set -Eeou pipefail

source scripts/dev/set_env_context.sh
source scripts/funcs/kubernetes
source scripts/funcs/install

DEPLOY_SCRIPT_PATH="./deploy/kubernetes-latest/deploy.sh"
HOST_PATH_VERSION="1.14.1"
EXTRACTED_DIR="/tmp/csi-driver-host-path-${HOST_PATH_VERSION}"

csi_driver_download() {
    echo "install resizable csi"
    REPO_URL="https://github.com/kubernetes-csi/csi-driver-host-path/archive/refs/tags/v${HOST_PATH_VERSION}.tar.gz"
    TAR_FILE="/tmp/csi-driver-host-path-v${HOST_PATH_VERSION}.tar.gz"

    echo "Downloading ${REPO_URL}..."
    curl_with_retry -L -o "${TAR_FILE}" "${REPO_URL}"

    echo "Extracting ${TAR_FILE}..."
    tar -xzf "${TAR_FILE}" -C /tmp
}

csi_driver_deploy() {
    local context="$1"

    # Subshell so the cd into EXTRACTED_DIR (required for the relative deploy
    # paths below) doesn't leak to the caller — recreate_kind_cluster.sh's
    # trailing `cp .generated/current.kubeconfig` relies on cwd staying put.
    (
    cd "${EXTRACTED_DIR}"
    SNAPSHOTTER_BRANCH=release-6.3

    kubectl_apply_retry --context "${context}" -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/${SNAPSHOTTER_BRANCH}/client/config/crd/snapshot.storage.k8s.io_volumesnapshotclasses.yaml
    kubectl_apply_retry --context "${context}" -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/${SNAPSHOTTER_BRANCH}/client/config/crd/snapshot.storage.k8s.io_volumesnapshotcontents.yaml
    kubectl_apply_retry --context "${context}" -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/${SNAPSHOTTER_BRANCH}/client/config/crd/snapshot.storage.k8s.io_volumesnapshots.yaml

    SNAPSHOTTER_VERSION=v6.3.3

    kubectl_apply_retry --context "${context}" -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/${SNAPSHOTTER_VERSION}/deploy/kubernetes/snapshot-controller/rbac-snapshot-controller.yaml
    kubectl_apply_retry --context "${context}" -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/${SNAPSHOTTER_VERSION}/deploy/kubernetes/snapshot-controller/setup-snapshot-controller.yaml

    # Run the deploy script with kubectl wrapped to force it to use specific context rather than rely on current context
    if ! run_script_with_wrapped_kubectl "${DEPLOY_SCRIPT_PATH}" "${context}"; then
        exit 1
    fi

    echo "Installing csi storageClass"
    kubectl apply --context "${context}" -f ./examples/csi-storageclass.yaml

    echo "Deployment successful on ${context}"
    )
}
