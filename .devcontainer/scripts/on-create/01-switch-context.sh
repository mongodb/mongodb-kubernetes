#!/bin/bash

# Regenerate .generated/context.export.env so PROJECT_DIR (and friends) reflect
# the container's filesystem rather than whatever the host last wrote. Must run
# before any on-create step that sources set_env_context.sh — otherwise scripts
# like recreate_python_venv.sh `cd` into a host-only path that doesn't exist
# inside the container.

set -euo pipefail

context=root-context
if [ -f "/workspace/.generated/.current_context" ]; then
    context=$(cat /workspace/.generated/.current_context)
fi
make switch context="${context}"
