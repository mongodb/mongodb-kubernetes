#!/usr/bin/env bash

# This scripts recreates local python venv in the ${PROJECT_DIR} directory from the current context.

set -Eeou pipefail

# Parse command line arguments
INSTALL_REQUIREMENTS=true

while [[ $# -gt 0 ]]; do
    case $1 in
        --skip-requirements)
            INSTALL_REQUIREMENTS=false
            shift
            ;;
        -h|--help)
            echo "Usage: $0 [--skip-requirements]"
            echo "  --skip-requirements    Skip installing requirements.txt"
            echo "  -h, --help            Show this help message"
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            echo "Use -h or --help for usage information"
            exit 1
            ;;
    esac
done

source scripts/dev/set_env_context.sh

# Ensure Python 3.10 is available, install if needed
ensure_python310() {
    echo "Checking current Python version..." >&2
    
    # Check if current python is 3.10
    if command -v python3 &> /dev/null; then
        local version
        if version=$(python3 --version 2>&1) && [[ "${version}" == *"Python 3.10"* ]]; then
            echo "Found Python 3.10: ${version}" >&2
            echo "python3"
            return 0
        else
            echo "Current python3 version: ${version}" >&2
        fi
    fi
    
    # Try to install Python 3.10 using pyenv if available
    if command -v pyenv &> /dev/null; then
        echo "Python 3.10 not found. Attempting to install via pyenv..." >&2
        
        # Check if any 3.10 version is already installed
        if pyenv versions --bare | grep -q "^3\.10\."; then
            local installed_version
            installed_version=$(pyenv versions --bare | grep "^3\.10\." | head -1)
            echo "Found existing pyenv Python 3.10: ${installed_version}" >&2
            pyenv global "${installed_version}"
            echo "python3"
            return 0
        fi
        
        # Install latest Python 3.10
        local latest_310
        latest_310=$(pyenv install --list | grep -E "^[[:space:]]*3\.10\.[0-9]+$" | tail -1 | xargs)
        if [[ -n "${latest_310}" ]]; then
            echo "Installing Python ${latest_310} via pyenv..." >&2
            if pyenv install "${latest_310}"; then
                pyenv global "${latest_310}"
                echo "python3"
                return 0
            fi
        fi
    fi
    
    echo "Error: No suitable Python 3.10 installation found and unable to install via pyenv." >&2
    echo "Please ensure Python 3.10 is installed or pyenv is available." >&2
    return 1
}

if [[ -d "${PROJECT_DIR}"/venv ]]; then
  echo "Removing venv..."
  cd "${PROJECT_DIR}"
  rm -rf "venv"
fi

# Ensure Python 3.10 is available
python_bin=$(ensure_python310)

echo "Using python from the following path: ${python_bin}"

"${python_bin}" -m venv venv
source venv/bin/activate
pip install --upgrade pip

if [[ "${INSTALL_REQUIREMENTS}" == "true" ]]; then
    echo "Installing requirements.txt..."
    pip install -r requirements.txt
else
    echo "Skipping requirements.txt installation (--skip-requirements flag used)"
fi

echo "Python venv was recreated successfully."
echo "Current python path: $(which python)"
python --version
