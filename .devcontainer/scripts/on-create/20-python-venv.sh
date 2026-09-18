#!/bin/bash

set -euo pipefail

# Run the TOOLING's copy (the worktree's own may predate portability fixes,
# e.g. rm -rf on the /workspace/venv mountpoint), with cwd=/workspace so its
# relative `source scripts/dev/set_env_context.sh` still resolves.
cd /workspace
"${MCK_TOOLING:-/mck-tooling}/scripts/dev/recreate_python_venv.sh"
