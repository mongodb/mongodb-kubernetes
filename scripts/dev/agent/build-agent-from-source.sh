#!/bin/bash
# Build agent from mms-automation source and switch context to use it
# Usage: ./build-agent-from-source.sh

set -e
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"
DOCKERFILE="${SCRIPT_DIR}/Dockerfile"
OVERRIDE_FILE="${SCRIPT_DIR}/.override"

MMS_AUTOMATION_PATH="${MMS_AUTOMATION_PATH:-${HOME}/projects/mms-automation}"
AGENT_TAG="${USER}-monarch"

# ECR registry
ECR_REGISTRY="268558157000.dkr.ecr.us-east-1.amazonaws.com"
ECR_REPO="staging/mongodb-agent"
FULL_IMAGE="${ECR_REGISTRY}/${ECR_REPO}:${AGENT_TAG}"

# Validate mms-automation path
if [[ ! -d "$MMS_AUTOMATION_PATH/go_planner" ]]; then
    echo "Error: MMS_AUTOMATION_PATH not found or invalid: $MMS_AUTOMATION_PATH"
    echo "Set MMS_AUTOMATION_PATH env var to your mms-automation checkout"
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
echo "Building agent from source"
echo "============================================"
echo "Source:     $MMS_AUTOMATION_PATH"
echo "Image:      $FULL_IMAGE"
echo "Context:    $CURRENT_CONTEXT"
echo "============================================"

# Get GitHub token
if [[ -z "$GITHUB_TOKEN" ]]; then
    if command -v gh &>/dev/null; then
        export GITHUB_TOKEN=$(gh auth token)
    else
        echo "ERROR: GITHUB_TOKEN not set and gh CLI not available"
        exit 1
    fi
fi

# AWS ECR login
echo ""
echo "Logging into AWS ECR..."
make -C "$PROJECT_DIR" aws_login

# Detect target platform (default to cluster platform, not local machine)
# For remote kind clusters on EC2, this is typically amd64
DOCKER_PLATFORM="${DOCKER_PLATFORM:-linux/amd64}"

# Build
echo ""
echo "Building image for $DOCKER_PLATFORM..."
docker build \
    --platform "$DOCKER_PLATFORM" \
    --build-arg CACHE_BUST="$(date +%s)" \
    --secret id=github_token,env=GITHUB_TOKEN \
    -f "$DOCKERFILE" \
    -t "$FULL_IMAGE" \
    "$MMS_AUTOMATION_PATH"

# Push
echo ""
echo "Pushing image..."
docker push "$FULL_IMAGE"

# Write temp override, switch context, then remove override
echo ""
echo "Switching context..."
echo "export MDB_AGENT_IMAGE=\"$FULL_IMAGE\"" > "$OVERRIDE_FILE"
trap "rm -f '$OVERRIDE_FILE'" EXIT
make -C "$PROJECT_DIR" switch context="$CURRENT_CONTEXT"

echo ""
echo "============================================"
echo "Done! Using agent: $FULL_IMAGE"
echo "============================================"
