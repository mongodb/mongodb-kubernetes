#!/usr/bin/env bash

# This scripts recreates local python venv in the ${PROJECT_DIR} directory from the current context.

set -Eeou pipefail -o posix

source scripts/dev/set_env_context.sh

if [[ -d "${PROJECT_DIR}"/venv ]]; then
  echo "Removing venv..."
  cd "${PROJECT_DIR}"
  rm -rf "venv"
fi

# in our EVG hosts, python versions are always in /opt/python
python_bin="/opt/python/${PYTHON_VERSION}/bin/python3"
if [[ "$(uname)" == "Darwin" ]]; then
  python_bin="python${PYTHON_VERSION}"
fi

echo "Using python from the following path: ${python_bin}"

"${python_bin}" -m venv venv
source venv/bin/activate
pip install --upgrade pip
pip install -r requirements.txt
echo "Python venv was recreated successfully."
echo "Current python path: $(which python)"
python --version
