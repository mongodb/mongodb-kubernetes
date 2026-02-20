#!/usr/bin/env bash

# This scripts recreates local python venv in the ${PROJECT_DIR} directory from the current context.

set -Eeou pipefail

source scripts/dev/set_env_context.sh

install_pyenv() {
      # Install python3-venv package for Debian/Ubuntu systems if needed
      if command -v apt-get &> /dev/null; then
          echo "Installing python3-venv package for venv support..." >&2
          sudo apt-get update -qq || true
          sudo apt-get install -y python3-venv || true
      fi

    # Check if pyenv directory exists first
    if [[ -d "${HOME}/.pyenv" ]]; then
        echo "pyenv directory already exists, setting up environment..." >&2
        export PYENV_ROOT="${HOME}/.pyenv"
        export PATH="${PYENV_ROOT}/bin:${PATH}"

        # Initialize pyenv in current shell
        if command -v pyenv &> /dev/null; then
            eval "$(pyenv init --path)"
            eval "$(pyenv init -)"
            echo "pyenv already installed and initialized" >&2
            return 0
        else
            echo "pyenv directory exists but binary not working, reinstalling..." >&2
            rm -rf "${HOME}/.pyenv"
        fi
    fi

    # Check if pyenv command is available in PATH
    if command -v pyenv &> /dev/null; then
        echo "pyenv already available in PATH" >&2
        return 0
    fi

    echo "Installing pyenv..." >&2

    # Install pyenv via the official installer
    if curl -s -S -L https://raw.githubusercontent.com/pyenv/pyenv-installer/master/bin/pyenv-installer | bash; then
        # Add pyenv to PATH for current session
        export PYENV_ROOT="${HOME}/.pyenv"
        export PATH="${PYENV_ROOT}/bin:${PATH}"

        # Initialize pyenv in current shell
        if command -v pyenv &> /dev/null; then
            eval "$(pyenv init --path)"
            eval "$(pyenv init -)"
        fi

        echo "pyenv installed successfully" >&2
        return 0
    else
        echo "Failed to install pyenv" >&2
        return 1
    fi
}

ensure_required_python() {
    if [[ -z "${PYTHON_VERSION:-}" ]]; then
        echo "Error: PYTHON_VERSION environment variable is not set or empty" >&2
        echo "PYTHON_VERSION should be set in root-context" >&2
        return 1
    fi

    local required_version="${PYTHON_VERSION}"

    echo "Setting up Python ${required_version}..." >&2

    if ! install_pyenv; then
        echo "Error: Failed to install pyenv" >&2
        return 1
    fi

    # Always update pyenv to ensure we have the latest Python versions available
    # On static hosts we might have a stale pyenv installation.
    echo "Updating pyenv to get latest Python versions..." >&2
    if [[ -d "${HOME}/.pyenv/.git" ]]; then
        cd "${HOME}/.pyenv" && git pull && cd - > /dev/null || echo "Warning: Failed to update pyenv via git" >&2
    fi

    # Check if the required version is already installed
    if pyenv versions --bare | grep -q "^${required_version}$"; then
        echo "Python ${required_version} already installed via pyenv" >&2
        pyenv global "${required_version}"
        return 0
    fi

    # Its not installed!
    echo "Installing Python ${required_version} via pyenv..." >&2
    if pyenv install "${required_version}"; then
        pyenv global "${required_version}"
        return 0
    else
        echo "Error: Failed to install Python ${required_version} via pyenv" >&2
        return 1
    fi
}

cd "${PROJECT_DIR}"
if [[ -d "venv" ]]; then
  echo "Removing existing venv..." >&2
  rm -rf "venv"
  echo "Existing venv removed" >&2
else
  echo "No existing venv found" >&2
fi

# Ensure required Python version is available
ensure_required_python

# Make sure we are using the correct Python version when setting up venv
PYENV_VERSION="${PYTHON_VERSION}" python -m venv venv
source venv/bin/activate
pip install --upgrade pip

echo "Installing requirements.txt..."
pip install -r requirements.txt

echo "Python venv was recreated successfully."
echo "Using Python: $(which python) ($(python --version))" >&2
