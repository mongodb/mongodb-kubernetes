#!/usr/bin/env bash
# Devcontainer feature: pyenv
#
# Installs pyenv and its system build dependencies into the container image.
# Because compose.yml mounts a named volume over ~/.pyenv, Docker seeds that
# volume from these image-layer contents on the first (and only the first) start
# of a fresh volume — so compiled Python versions survive `Rebuild Container`.
#
# The on-create script (scripts/dev/recreate_python_venv.sh) detects the
# pre-installed pyenv, skips the installer, and proceeds directly to
# `pyenv install <version>` + venv creation.

set -euo pipefail

REMOTE_USER="${_REMOTE_USER:-vscode}"
REMOTE_USER_HOME="/home/${REMOTE_USER}"
PYENV_ROOT="${REMOTE_USER_HOME}/.pyenv"

# ---------------------------------------------------------------------------
# 1. System build dependencies required to compile Python via pyenv
# ---------------------------------------------------------------------------
echo "Installing pyenv build dependencies..."
apt-get update -y
apt-get install -y --no-install-recommends \
    build-essential \
    ca-certificates \
    curl \
    git \
    libbz2-dev \
    libffi-dev \
    liblzma-dev \
    libncurses-dev \
    libreadline-dev \
    libsqlite3-dev \
    libssl-dev \
    libxml2-dev \
    libxmlsec1-dev \
    make \
    tk-dev \
    xz-utils \
    zlib1g-dev
rm -rf /var/lib/apt/lists/*

# ---------------------------------------------------------------------------
# 2. Install the pyenv binary as the container user (so it lands in ~/.pyenv)
# ---------------------------------------------------------------------------
echo "Installing pyenv for user '${REMOTE_USER}'..."
su - "${REMOTE_USER}" -c 'curl -fsSL https://pyenv.run | bash'

# Ensure all pyenv files are owned by the container user.
# su in a Docker build layer does not reliably preserve uid, so files can end
# up root-owned; this guarantees the user can write shims and version dirs.
chown -R "${REMOTE_USER}:${REMOTE_USER}" "${PYENV_ROOT}"

# ---------------------------------------------------------------------------
# 3. Wire pyenv into all login and interactive shells via /etc/profile.d
# ---------------------------------------------------------------------------
# Use a placeholder so the heredoc can be single-quoted (no accidental
# expansion of ${PATH} or the $(...) subshells at write time).
cat > /etc/profile.d/pyenv.sh << 'PROFILE'
export PYENV_ROOT="__PYENV_ROOT__"
export PATH="${PYENV_ROOT}/bin:${PATH}"
if command -v pyenv > /dev/null 2>&1; then
    eval "$(pyenv init --path)"
    eval "$(pyenv init -)"
fi
PROFILE
sed -i "s|__PYENV_ROOT__|${PYENV_ROOT}|g" /etc/profile.d/pyenv.sh
chmod +x /etc/profile.d/pyenv.sh

# ---------------------------------------------------------------------------
# 4. Pre-install the requested Python version (if provided)
# ---------------------------------------------------------------------------
# The devcontainer feature spec uppercases option names: pythonVersion → PYTHONVERSION
if [[ -n "${PYTHONVERSION:-}" && "${PYTHONVERSION}" != "none" ]]; then
    echo "Pre-installing Python ${PYTHONVERSION} via pyenv..."
    su - "${REMOTE_USER}" -c "
        export PYENV_ROOT=\"${PYENV_ROOT}\"
        export PATH=\"${PYENV_ROOT}/bin:\${PATH}\"
        eval \"\$(pyenv init --path)\"
        eval \"\$(pyenv init -)\"
        pyenv install --skip-existing \"${PYTHONVERSION}\"
        pyenv global \"${PYTHONVERSION}\"
    "
    echo "Python ${PYTHONVERSION} installed and set as global default"
fi

echo "pyenv successfully installed to ${PYENV_ROOT}"
