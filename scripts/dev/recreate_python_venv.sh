#!/usr/bin/env bash

# This scripts recreates local python venv in the ${PROJECT_DIR} directory from the current context.

set -Eeou pipefail

ensure_required_python() {
    local required_version="${PYTHON_VERSION:-3.10}"
    local major_minor
    major_minor=$(echo "${required_version}" | grep -oE '^[0-9]+\.[0-9]+')

    echo "Checking for Python ${required_version} (${major_minor}.x)..." >&2

    # Check if current python matches required version
    if command -v python3 &> /dev/null; then
        local version
        if version=$(python3 --version 2>&1) && [[ "${version}" == *"Python ${major_minor}"* ]]; then
            echo "Found Python ${major_minor}: ${version}" >&2
            echo "python3"
            return 0
        else
            echo "Current python3 version: ${version}" >&2
        fi
    fi

    # Try to install required Python version using pyenv if available
    if command -v pyenv &> /dev/null; then
        echo "Python ${major_minor} not found. Attempting to install via pyenv..." >&2

        # Check if any version in the required series is already installed
        if pyenv versions --bare | grep -q "^${major_minor}\."; then
            local installed_version
            installed_version=$(pyenv versions --bare | grep "^${major_minor}\." | head -1)
            echo "Found existing pyenv Python ${major_minor}: ${installed_version}" >&2
            pyenv global "${installed_version}"
            echo "python3"
            return 0
        fi

        # Install latest version in the required series
        local latest_version
        latest_version=$(pyenv install --list | grep -E "^[[:space:]]*${major_minor}\.[0-9]+$" | tail -1 | xargs)
        if [[ -n "${latest_version}" ]]; then
            echo "Installing Python ${latest_version} via pyenv..." >&2
            if pyenv install "${latest_version}"; then
                pyenv global "${latest_version}"
                echo "python3"
                return 0
            fi
        fi
    fi

    echo "Error: No suitable Python ${major_minor} installation found and unable to install via pyenv." >&2
    echo "Please ensure Python ${major_minor} is installed or pyenv is available." >&2
    return 1
}

if [[ -d "${PROJECT_DIR}"/venv ]]; then
  echo "Removing venv..."
  cd "${PROJECT_DIR}"
  rm -rf "venv"
fi

# Ensure required Python version is available
python_bin=$(ensure_required_python)

echo "Using python from the following path: ${python_bin}"

"${python_bin}" -m venv venv
source venv/bin/activate
pip install --upgrade pip
echo "Installing requirements.txt..."
pip install -r requirements.txt
echo "Python venv was recreated successfully."
echo "Current python path: $(which python)"
python --version
