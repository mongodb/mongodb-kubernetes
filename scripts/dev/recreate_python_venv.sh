#!/usr/bin/env bash

# This script recreates local python venv using uv in the ${PROJECT_DIR} directory.
# For the pip-based alternative, see recreate_python_venv_pip.sh.

set -Eeou pipefail

source scripts/dev/set_env_context.sh

install_uv() {
    if command -v uv &> /dev/null; then
        echo "uv already available in PATH" >&2
        return 0
    fi

    echo "Installing uv..." >&2
    if curl -LsSf https://astral.sh/uv/install.sh | sh; then
        # Add common uv install locations to PATH for current session
        export PATH="${HOME}/.local/bin:${HOME}/.cargo/bin:${PATH}"
        if command -v uv &> /dev/null; then
            echo "uv installed successfully" >&2
            return 0
        fi
    fi

    echo "Failed to install uv" >&2
    return 1
}

ensure_required_python() {
    if [[ -z "${PYTHON_VERSION:-}" ]]; then
        echo "Error: PYTHON_VERSION environment variable is not set or empty" >&2
        echo "PYTHON_VERSION should be set in root-context" >&2
        return 1
    fi

    echo "Ensuring Python ${PYTHON_VERSION} is available..." >&2

    # If Python is already available (e.g. via Homebrew or pyenv), skip download
    if uv python find "${PYTHON_VERSION}" &> /dev/null; then
        echo "Python ${PYTHON_VERSION} already available" >&2
        return 0
    fi

    echo "Installing Python ${PYTHON_VERSION} via uv..." >&2
    uv python install "${PYTHON_VERSION}"
}

cd "${PROJECT_DIR}"
if [[ -d "venv" ]]; then
    echo "Removing existing venv..." >&2
    rm -rf "venv"
    echo "Existing venv removed" >&2
else
    echo "No existing venv found" >&2
fi

install_uv

ensure_required_python

echo "Creating venv with Python ${PYTHON_VERSION} using uv..."
uv venv venv --python "${PYTHON_VERSION}"
source venv/bin/activate

echo "Installing requirements.txt..."
uv pip install -r requirements.txt

echo "Python venv was recreated successfully."
echo "Using Python: $(which python) ($(python --version))" >&2
