#!/bin/bash
# Build Monarch from source and switch context to use it
# Usage: ./build-monarch-from-source.sh

set -e
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "${SCRIPT_DIR}/../../.." && pwd)"

MONARCH_PATH="${MONARCH_PATH:-${HOME}/projects/monarch}"
MONARCH_TAG="${USER}-monarch"

# Default to linux/amd64 for Evergreen compatibility
DOCKER_PLATFORM="${DOCKER_PLATFORM:-linux/amd64}"

# ECR registry
ECR_REGISTRY="268558157000.dkr.ecr.us-east-1.amazonaws.com"
ECR_REPO="staging/mongodb-kubernetes-monarch-injector"
FULL_IMAGE="${ECR_REGISTRY}/${ECR_REPO}:${MONARCH_TAG}"

# Validate monarch path
if [[ ! -f "${MONARCH_PATH}/go.mod" ]]; then
    echo "Error: MONARCH_PATH not found or invalid: ${MONARCH_PATH}"
    echo "Set MONARCH_PATH env var to your monarch checkout"
    exit 1
fi

# Check current context exists
CURRENT_CONTEXT_FILE="${PROJECT_DIR}/.generated/.current_context"
if [[ ! -f "${CURRENT_CONTEXT_FILE}" ]]; then
    echo "Error: No current context. Run 'make switch context=...' first."
    exit 1
fi
CURRENT_CONTEXT=$(cat "${CURRENT_CONTEXT_FILE}")

echo "============================================"
echo "Building Monarch from source"
echo "============================================"
echo "Source:     ${MONARCH_PATH}"
echo "Image:      ${FULL_IMAGE}"
echo "Platform:   ${DOCKER_PLATFORM}"
echo "Context:    ${CURRENT_CONTEXT}"
echo "============================================"

# AWS ECR login
echo ""
echo "Logging into AWS ECR..."
make -C "${PROJECT_DIR}" aws_login

# Use pipeline.py to build and push (handles cross-compilation properly)
echo ""
echo "Building and pushing monarch image..."
cd "${PROJECT_DIR}"
python scripts/release/pipeline.py \
    --image monarch-injector \
    --scenario staging \
    --monarch-path "${MONARCH_PATH}" \
    --platform "${DOCKER_PLATFORM}" \
    --version "${MONARCH_TAG}"

# Write temp override, switch context, then remove override
echo ""
echo "Switching context..."
OVERRIDE_FILE="${PROJECT_DIR}/scripts/dev/contexts/private-context-monarch"
echo "export MDB_MONARCH_IMAGE=\"${FULL_IMAGE}\"" > "${OVERRIDE_FILE}"
trap 'rm -f "${OVERRIDE_FILE}"' EXIT
make -C "${PROJECT_DIR}" switch context="${CURRENT_CONTEXT}" additional_override=private-context-monarch

echo ""
echo "============================================"
echo "Done! Using Monarch: ${FULL_IMAGE}"
echo "============================================"
