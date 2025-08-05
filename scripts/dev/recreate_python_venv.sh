#!/usr/bin/env bash

# This scripts recreates local python venv in the ${PROJECT_DIR} directory from the current context.

set -Eeou pipefail

source scripts/dev/set_env_context.sh

install_pyenv() {
    if command -v pyenv &> /dev/null; then
        echo "pyenv already installed" >&2
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
    local required_version="${PYTHON_VERSION:-3.13}"
    local major_minor
    major_minor=$(echo "${required_version}" | grep -oE '^[0-9]+\.[0-9]+')

    echo "Setting up Python ${required_version} (${major_minor}.x)..." >&2

    # Always install pyenv first
    if ! install_pyenv; then
        echo "Error: Failed to install pyenv" >&2
        return 1
    fi

    # Install latest version in the required series
    local latest_version
    latest_version=$(pyenv install --list | grep -E "^[[:space:]]*${major_minor}\.[0-9]+$" | tail -1 | xargs)
    if [[ -n "${latest_version}" ]]; then
        echo "Installing Python ${latest_version} via pyenv..." >&2
        # Use --skip-existing to avoid errors if version already exists
        if pyenv install --skip-existing "${latest_version}"; then
            pyenv global "${latest_version}"
            # Install python3-venv package for Debian/Ubuntu systems if needed
            if command -v apt-get &> /dev/null; then
                echo "Installing python3-venv package for venv support..." >&2
                sudo apt-get update -qq && sudo apt-get install -y python3-venv || true
            fi
            return 0
        fi
    fi

    echo "Error: Unable to install Python ${major_minor} via pyenv" >&2
    return 1
}

if [[ -d "${PROJECT_DIR}"/venv ]]; then
  echo "Removing venv..."
  cd "${PROJECT_DIR}"
  rm -rf "venv"
fi

# Ensure required Python version is available
ensure_required_python

python3 -m venv venv
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
