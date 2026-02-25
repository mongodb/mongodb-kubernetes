#!/bin/bash

set -euo pipefail

K8S_AI_TOOLS_DIR="/opt/k8s-ai-tools"

# Clone k8s-ai-tools repository
if git clone "https://github.com/10gen/k8s-ai-tools.git" "${K8S_AI_TOOLS_DIR}" 2>/dev/null; then
    source /workspace/venv/bin/activate
    pip install -r "${K8S_AI_TOOLS_DIR}/requirements.txt"

    if [ -d "${K8S_AI_TOOLS_DIR}/.claude" ]; then
        # remove the context-and-env-vars skill because the devcontainer already has its own setup
        rm -rf "${K8S_AI_TOOLS_DIR}/.claude/skills/context-and-env-vars"

        ln -sf "${K8S_AI_TOOLS_DIR}/.claude" "${HOME}/.claude"
        echo "Symlinked .claude folder to ${HOME}/.claude"
    fi

    if [ -d "${K8S_AI_TOOLS_DIR}/.cursor" ]; then
        ln -sf "${K8S_AI_TOOLS_DIR}/.cursor" "${HOME}/.cursor"
        echo "Symlinked .cursor folder to ${HOME}/.cursor"
    fi

    tee -a "${HOME}/.bashrc" > /dev/null <<EOF
export PYTHONPATH="${PYTHONPATH:+${PYTHONPATH}:}${K8S_AI_TOOLS_DIR}"
EOF
    echo "Added ${K8S_AI_TOOLS_DIR} to PYTHONPATH"
else
    echo "Warning: Failed to clone k8s-ai-tools repository"
fi
