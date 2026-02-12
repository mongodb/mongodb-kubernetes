#!/bin/bash

set -euo pipefail

# Switch to the root context once, to kickstart the environment
make switch context=root-context

echo "source /workspace/.generated/context.export.env" >> /home/vscode/.bashrc
