#!/bin/bash

# Regenerate .generated/context.env + context.devc.env so PROJECT_DIR (and
# friends) reflect the container's filesystem rather than whatever the host
# last wrote. Must run before any on-create step that sources
# scripts/dev/devenv (or set_env_context.sh, which wraps it) — otherwise
# scripts like recreate_python_venv.sh `cd` into a host-only path that
# doesn't exist inside the container.

set -euo pipefail

# Tooling-relative: /workspace may be a worktree without the devcontainer
# tooling; run the tooling's switch_context.sh (it targets the cwd worktree
# and emits master-compat artifacts alongside the per-side files).
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

context=root-context
if [ -f "/workspace/.generated/.current_context" ]; then
    context=$(cat /workspace/.generated/.current_context)
fi
cd /workspace
"${SCRIPT_DIR}/../../../scripts/dev/switch_context.sh" "${context}"
