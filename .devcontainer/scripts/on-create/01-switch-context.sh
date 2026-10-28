#!/bin/bash

# Write context.devc.env so PROJECT_DIR and the other paths name the container's filesystem. Must run
# before any on-create step that sources scripts/dev/devenv.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

context=root-context
if [ -f "/workspace/.generated/.current_context" ]; then
    context=$(cat /workspace/.generated/.current_context)
fi
cd /workspace
"${SCRIPT_DIR}/../../../scripts/dev/switch_context.sh" "${context}"
