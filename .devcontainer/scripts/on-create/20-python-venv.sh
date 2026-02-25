#!/bin/bash

set -euo pipefail

/workspace/scripts/dev/recreate_python_venv.sh

echo "source /workspace/venv/bin/activate" >> /home/vscode/.bashrc
