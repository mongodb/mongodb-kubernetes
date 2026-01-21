#!/bin/bash
# Periodically execute search index creation to generate traffic for testing/debugging
# This is useful for capturing traffic through Envoy proxy

set -euo pipefail

# Configuration
INTERVAL="${INTERVAL:-5}"    # seconds between executions
MAX_RUNS="${MAX_RUNS:-0}"    # 0 = infinite

# Get script directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Source environment variables
source "${SCRIPT_DIR}/env_variables.sh"

echo "=== Periodic Search Traffic Generator ==="
echo "Interval: ${INTERVAL}s"
echo "Max runs: $([ "${MAX_RUNS}" -eq 0 ] && echo "unlimited" || echo "${MAX_RUNS}")"
echo
echo "Press Ctrl+C to stop"
echo

# Cleanup on exit
trap 'echo -e "\nStopping..."; exit 0' SIGINT SIGTERM

run_count=0

while true; do
    run_count=$((run_count + 1))

    echo "--- Run #${run_count} at $(date +%H:%M:%S) ---"

    # Execute the search index creation script
    bash "${SCRIPT_DIR}/code_snippets/03_0430_create_search_index.sh"

    echo

    # Check if we've reached max runs
    if [ "${MAX_RUNS}" -gt 0 ] && [ "${run_count}" -ge "${MAX_RUNS}" ]; then
        echo "Reached maximum runs (${MAX_RUNS}). Exiting."
        exit 0
    fi

    # Wait for next iteration
    sleep "${INTERVAL}"
done