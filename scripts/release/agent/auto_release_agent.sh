#!/bin/bash
set -euo pipefail

# Auto-release agent images to Quay if new agents are detected in release.json
# This script replaces the PCT-triggered release flow (CLOUDP-305848).
#
# How it works:
# 1. Fetches all tags from Quay in one API call (using skopeo)
# 2. Compares against all agents in release.json
# 3. If any agents are missing from Quay, triggers the release pipeline
#
# Exit codes:
#   0 - Success (either released or nothing to release)
#   1 - Failure during release
#   2 - Could not determine missing agents

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"

cd "${PROJECT_ROOT}"

# Source environment context for release configuration
source scripts/dev/set_env_context.sh

echo "=========================================="
echo "Auto Release Agent - Checking for missing agents in Quay"
echo "=========================================="

# Run the detection script
scripts/dev/run_python.sh scripts/release/agent/check_agents_in_quay.py
CHECK_RESULT=$?

if [[ ${CHECK_RESULT} -eq 0 ]]; then
    echo ""
    echo "No agents to release - all agents already in Quay"
    exit 0
elif [[ ${CHECK_RESULT} -eq 2 ]]; then
    echo ""
    echo "ERROR: Could not fetch Quay tags - cannot determine missing agents"
    exit 2
fi

# If we get here, CHECK_RESULT is 1 meaning there are missing agents
echo ""
echo "=========================================="
echo "Releasing missing agents to Quay..."
echo "=========================================="

# Run the pipeline to release agents
# Set environment variables expected by pipeline.sh
export IMAGE_NAME="agent"
export BUILD_SCENARIO_OVERRIDE="release"

# The pipeline will use detect_ops_manager_changes() to find agents to build
# and skip_if_exists will prevent re-releasing existing ones
scripts/dev/run_python.sh scripts/release/pipeline.py agent --build-scenario release

echo ""
echo "=========================================="
echo "Agent release complete"
echo "=========================================="
