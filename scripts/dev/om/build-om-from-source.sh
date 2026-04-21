#!/bin/bash
# Build Ops Manager from ops-manager source and switch context to use it
# Usage: ./build-om-from-source.sh

set -e
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"
DOCKERFILE_DIR="${PROJECT_DIR}/docker/mongodb-enterprise-ops-manager"

OPS_MANAGER_PATH="${OPS_MANAGER_PATH:-${HOME}/projects/ops-manager}"
OM_TAG="${USER}-om-local"

# Default to linux/amd64 for Evergreen compatibility
DOCKER_PLATFORM="${DOCKER_PLATFORM:-linux/amd64}"

# ECR registry
ECR_REGISTRY="268558157000.dkr.ecr.us-east-1.amazonaws.com"
ECR_REPO="staging/mongodb-enterprise-ops-manager-ubi"
FULL_IMAGE="${ECR_REGISTRY}/${ECR_REPO}:${OM_TAG}"

# Validate ops-manager path
if [[ ! -f "$OPS_MANAGER_PATH/WORKSPACE" ]]; then
    echo "Error: OPS_MANAGER_PATH not found or invalid: $OPS_MANAGER_PATH"
    echo "Set OPS_MANAGER_PATH env var to your ops-manager checkout"
    exit 1
fi

# Check current context exists
CURRENT_CONTEXT_FILE="${PROJECT_DIR}/.generated/.current_context"
if [[ ! -f "$CURRENT_CONTEXT_FILE" ]]; then
    echo "Error: No current context. Run 'make switch context=...' first."
    exit 1
fi
CURRENT_CONTEXT=$(cat "$CURRENT_CONTEXT_FILE")

echo "============================================"
echo "Building Ops Manager from source"
echo "============================================"
echo "Source:     $OPS_MANAGER_PATH"
echo "Image:      $FULL_IMAGE"
echo "Context:    $CURRENT_CONTEXT"
echo "============================================"

# AWS ECR login
echo ""
echo "Logging into AWS ECR..."
make -C "$PROJECT_DIR" aws_login

# Build tarball with Bazel (--build_env=tarball includes JDK and uses mongodb-mms/ wrapper dir)
echo ""
echo "Building OM tarball with Bazel..."
cd "$OPS_MANAGER_PATH"
./bazelisk build //server:package --build_env=tarball

# Copy tarball to project root (docker build context)
echo ""
echo "Copying tarball to build context..."
cp "$OPS_MANAGER_PATH/bazel-bin/server/package.tar.gz" "$PROJECT_DIR/"

# Build Docker image using existing Dockerfile with om_download_url=local
echo ""
echo "Building Docker image for $DOCKER_PLATFORM..."
docker build \
    --platform "$DOCKER_PLATFORM" \
    --build-arg version="${OM_TAG}" \
    --build-arg om_download_url=local \
    -f "$DOCKERFILE_DIR/Dockerfile" \
    -t "$FULL_IMAGE" \
    "$PROJECT_DIR"

# Push
echo ""
echo "Pushing image..."
docker push "$FULL_IMAGE"

# Write temp override, switch context, then remove override
# Uses existing additional_override mechanism in switch_context.sh
# MDB_OM_IMAGE is a full image override that bypasses version logic in the operator
echo ""
echo "Switching context..."
OVERRIDE_FILE="${PROJECT_DIR}/scripts/dev/contexts/private-context-om"
echo "export MDB_OM_IMAGE=\"$FULL_IMAGE\"" > "$OVERRIDE_FILE"
trap "rm -f '$OVERRIDE_FILE' '$PROJECT_DIR/package.tar.gz'" EXIT
make -C "$PROJECT_DIR" switch context="$CURRENT_CONTEXT" additional_override=private-context-om

echo ""
echo "============================================"
echo "Done! Using Ops Manager: $FULL_IMAGE"
echo "============================================"
