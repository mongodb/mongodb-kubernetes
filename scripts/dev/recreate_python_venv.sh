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

        # Fix permissions on pyenv binary if needed
        if [[ -f "${PYENV_ROOT}/bin/pyenv" ]]; then
            chmod +x "${PYENV_ROOT}/bin/pyenv"
        fi

        # Test if pyenv actually works
        if command -v pyenv &> /dev/null && pyenv --version &> /dev/null; then
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

        # Fix permissions on pyenv binary if needed
        if [[ -f "${PYENV_ROOT}/bin/pyenv" ]]; then
            chmod +x "${PYENV_ROOT}/bin/pyenv"
        fi

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
    # If PYTHON_VERSION is set (e.g., "3.13"), find the latest patch version
    # If not set, default to a specific version for consistency
    local base_version="${PYTHON_VERSION:-3.13}"
    local required_version

    # If PYTHON_VERSION contains a patch version (e.g., "3.13.7"), use it as-is
    if [[ "${base_version}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
        required_version="${base_version}"
    else
        # For major.minor versions (e.g., "3.13"), use a specific patch version
        case "${base_version}" in
            "3.13")
                required_version="3.13.7"
                ;;
            *)
                # Default fallback - try to find latest available version
                required_version="${base_version}.0"
                ;;
        esac
    fi

    echo "Setting up Python ${required_version} (base version: ${base_version})..." >&2

    if ! install_pyenv; then
        echo "Error: Failed to install pyenv" >&2
        return 1
    fi

    # Install specific pinned version for consistency across runs
    echo "Installing Python ${required_version} via pyenv..." >&2
    # Use --skip-existing to avoid errors if version already exists
    if pyenv install --skip-existing "${required_version}"; then
        pyenv global "${required_version}"
        echo "Python ${required_version} installed and set as global version" >&2
        return 0
    else
        echo "Error: Failed to install Python ${required_version}" >&2
        return 1
    fi
}

if [[ -d "${PROJECT_DIR}"/venv ]]; then
  echo "Removing venv..."
  cd "${PROJECT_DIR}"
  rm -rf "venv"
fi

# Ensure required Python version is available
ensure_required_python

# Ensure we're using the pyenv-managed Python for venv creation
if command -v pyenv &> /dev/null; then
    # Initialize pyenv in current shell to ensure we use the right Python
    eval "$(pyenv init --path)"
    eval "$(pyenv init -)"

    # Verify we're using the expected Python version
    current_python=$(python --version 2>&1 | awk '{print $2}')
    echo "Using Python version: ${current_python}" >&2
    echo "Python path: $(which python)" >&2

    # Use the pyenv-managed python explicitly
    python -m venv venv
else
    # Fallback to python3 if pyenv is not available
    echo "pyenv not available, using system python3" >&2
    python3 -m venv venv
fi
source venv/bin/activate
pip install --upgrade pip

skip_requirements="${SKIP_INSTALL_REQUIREMENTS:-false}"
if [[ "${skip_requirements}" != "true" ]]; then
    echo "Installing requirements.txt..."
    pip install -r requirements.txt
else
    echo "Skipping requirements.txt installation."
    pip install requests
fi

echo "Python venv was recreated successfully."
echo "Current python path: $(which python)"
python --version
