#!/usr/bin/env bash

# This script recreates local python venv using uv in the ${PROJECT_DIR} directory.
# For the pip-based alternative, see recreate_python_venv_pip.sh.

set -Eeou pipefail

source scripts/dev/set_env_context.sh

install_uv() {
    if [[ "${RUNNING_IN_EVG:-}" == "true" ]]; then
        # On Evergreen we always install uv, regardless of whether it's already available on the build host.
        # That way we have consistent behavior across all build hosts and we can be sure that uv is always able to find the right Python version from the astral prebuilds.
        echo "Running in Evergreen, forcing uv to install everything under ${PROJECT_DIR}" >&2
        export UV_UNMANAGED_INSTALL="${PROJECT_DIR}/.uv/bin"
        export UV_PYTHON_INSTALL_DIR=${PROJECT_DIR}/.uv/python
        export UV_CACHE_DIR=${PROJECT_DIR}/.uv/cache
        export UV_PYTHON_INSTALL_BIN=false
        export PATH="${PROJECT_DIR}/.uv/bin:${PATH}"
    elif command -v uv &> /dev/null && uv --version &> /dev/null; then
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

install_uv

ensure_required_python

echo "Creating venv with Python ${PYTHON_VERSION} using uv..."
uv venv venv --python "${PYTHON_VERSION}" --clear

# uv's python build statically link OpenSSL and that might get confused by the system-wide OpenSSL config.
# see https://github.com/astral-sh/python-build-standalone/issues/999
echo "export OPENSSL_CONF=/dev/null" >> venv/bin/activate

source venv/bin/activate

echo "Installing requirements.txt..."
uv pip install -r requirements.txt

echo "Python venv was recreated successfully."
echo "Using Python: $(which python) ($(python --version))" >&2
