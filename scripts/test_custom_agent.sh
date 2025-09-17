#!/bin/bash

set -euo pipefail

# Test script for building custom agent image
# This script demonstrates how to use the new --custom-agent-url parameter

CUSTOM_AGENT_URL="https://mciuploads.s3.amazonaws.com/mms-automation/mongodb-mms-build-agent/builds/patches/68c81e93cc2aec0007640bad/automation-agent/local/mongodb-mms-automation-agent-13.41.0.9772-1.linux_x86_64.tar.gz"

echo "Testing custom agent build with URL: ${CUSTOM_AGENT_URL}"
echo ""
echo "This will build the MongoDB agent image using your custom agent version."
echo "The image will be tagged with the registry specified in build_info.json for the current build scenario."
echo ""

# Use the existing pipeline with the new custom agent URL parameter
echo "Running: scripts/dev/run_python.sh scripts/release/pipeline_main.py agent --custom-agent-url \"${CUSTOM_AGENT_URL}\""
echo ""

# Execute the build
scripts/dev/run_python.sh scripts/release/pipeline_main.py agent --custom-agent-url "${CUSTOM_AGENT_URL}"

echo ""
echo "Custom agent build completed!"
echo "The image should now be available with your custom agent version 13.41.0.9772-1"
