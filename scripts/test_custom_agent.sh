#!/bin/bash

set -euo pipefail

# Test script for building custom agent image
# This script demonstrates how to use the new --custom-agent-url parameter
# It leverages the pipeline_main.py by supplying a version (latest) which is used by atomic_pipeline.py

CUSTOM_AGENT_URL="https://mciuploads.s3.amazonaws.com/mms-automation/mongodb-mms-build-agent/builds/patches/68c96f4020b54e00079b0621/automation-agent/local/mongodb-mms-automation-agent-13.41.0.9776-1.linux_x86_64.tar.gz"
VERSION="latest"

echo "Testing custom agent build with URL: ${CUSTOM_AGENT_URL}"
echo "Using version: ${VERSION}"
echo ""
echo "This will build the MongoDB agent image using your custom agent version."
echo "The image will be tagged with version '${VERSION}' and pushed to the registry specified in build_info.json."
echo ""

# Use the existing pipeline with version and custom agent URL parameters
echo "Running: scripts/dev/run_python.sh scripts/release/pipeline_main.py agent --version \"${VERSION}\" --custom-agent-url \"${CUSTOM_AGENT_URL}\""
echo ""

# Execute the build (add --load to build locally without pushing)
scripts/dev/run_python.sh scripts/release/pipeline_main.py agent --version "${VERSION}" --custom-agent-url "${CUSTOM_AGENT_URL}"

echo ""
echo "Custom agent build completed!"
echo "The image should now be available with tag '${VERSION}' containing your custom agent version 13.41.0.9772-1"
