#!/bin/bash

set -euo pipefail

# The venv lives in the venv volume (PROJECT_VENV_PATH in compose.yml), not in the host's checkout.
cd /workspace
scripts/dev/recreate_python_venv.sh
