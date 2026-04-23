#!/bin/bash
# Build custom images from source and switch context to use them
# Usage: ./build-custom-images.sh [--agent] [--om] [--monarch] [--all]
#
# Environment variables:
#   MMS_AUTOMATION_PATH  - Path to mms-automation checkout (default: ~/projects/mms-automation)
#   OPS_MANAGER_PATH     - Path to ops-manager checkout (default: ~/projects/ops-manager)
#   MONARCH_PATH         - Path to monarch checkout (default: ~/projects/monarch)
#   DOCKER_PLATFORM      - Target platform (default: linux/amd64)

set -e
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"

# Defaults
BUILD_AGENT=false
BUILD_OM=false
BUILD_MONARCH=false

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --agent)
            BUILD_AGENT=true
            shift
            ;;
        --om)
            BUILD_OM=true
            shift
            ;;
        --monarch)
            BUILD_MONARCH=true
            shift
            ;;
        --all)
            BUILD_AGENT=true
            BUILD_OM=true
            BUILD_MONARCH=true
            shift
            ;;
        -h|--help)
            echo "Usage: $0 [--agent] [--om] [--monarch] [--all]"
            echo ""
            echo "Build custom images from local source and configure context to use them."
            echo ""
            echo "Options:"
            echo "  --agent    Build agent from MMS_AUTOMATION_PATH (default: ~/projects/mms-automation)"
            echo "  --om       Build Ops Manager from OPS_MANAGER_PATH (default: ~/projects/ops-manager)"
            echo "  --monarch  Build Monarch from MONARCH_PATH (default: ~/projects/monarch)"
            echo "  --all      Build all three"
            echo ""
            echo "Environment variables:"
            echo "  MMS_AUTOMATION_PATH  Path to mms-automation checkout"
            echo "  OPS_MANAGER_PATH     Path to ops-manager checkout"
            echo "  MONARCH_PATH         Path to monarch checkout"
            echo "  DOCKER_PLATFORM      Target platform (default: linux/amd64)"
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            exit 1
            ;;
    esac
done

# If nothing selected, show help
if [[ "$BUILD_AGENT" == "false" && "$BUILD_OM" == "false" && "$BUILD_MONARCH" == "false" ]]; then
    echo "No images selected. Use --agent, --om, --monarch, or --all"
    echo "Run with --help for more info."
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
echo "Building custom images"
echo "============================================"
echo "Context:    ${CURRENT_CONTEXT}"
echo "Platform:   ${DOCKER_PLATFORM:-linux/amd64}"
echo ""
echo "Selected:"
[[ "$BUILD_AGENT" == "true" ]] && echo "  - Agent    (${MMS_AUTOMATION_PATH:-~/projects/mms-automation})"
[[ "$BUILD_OM" == "true" ]] && echo "  - OM       (${OPS_MANAGER_PATH:-~/projects/ops-manager})"
[[ "$BUILD_MONARCH" == "true" ]] && echo "  - Monarch  (${MONARCH_PATH:-~/projects/monarch})"
echo "============================================"

# AWS ECR login (once for all builds)
echo ""
echo "Logging into AWS ECR..."
make -C "${PROJECT_DIR}" aws_login

# Build selected images
if [[ "$BUILD_AGENT" == "true" ]]; then
    echo ""
    echo ">>> Building Agent..."
    "${SCRIPT_DIR}/agent/build-agent-from-source.sh"
fi

if [[ "$BUILD_OM" == "true" ]]; then
    echo ""
    echo ">>> Building Ops Manager..."
    "${SCRIPT_DIR}/om/build-om-from-source.sh"
fi

if [[ "$BUILD_MONARCH" == "true" ]]; then
    echo ""
    echo ">>> Building Monarch..."
    "${SCRIPT_DIR}/monarch/build-monarch-from-source.sh"
fi

echo ""
echo "============================================"
echo "Done! All selected images built and configured."
echo "============================================"
