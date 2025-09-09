#!/usr/bin/env bash

set -Eeou pipefail

source scripts/dev/set_env_context.sh
source scripts/funcs/printing

# shellcheck disable=SC2154
if [ -f "${PROJECT_DIR}/venv/bin/activate" ]; then
    source "${PROJECT_DIR}/venv/bin/activate"
else
  echo "Cannot find python venv in ${PROJECT_DIR}"
  ls -al "${PROJECT_DIR}"
  exit 1
fi

export PYTHONPATH="${PROJECT_DIR}"

required_version="${PYTHON_VERSION}"
if [[ -z "${required_version:-}" ]]; then
  echo -e "${RED}Error: PYTHON_VERSION environment variable is not set or empty${NO_COLOR}"
  echo -e "${RED}PYTHON_VERSION should be set in root-context${NO_COLOR}"
  exit 1
fi

current_python_version=$(python --version 2>&1 | awk '{print $2}')
if [[ "${current_python_version}" != "${required_version}" ]]; then
  echo -e "${RED}Detected mismatched version of python in your venv (detected version: ${current_python_version}).${NO_COLOR}"
  echo -e "${RED}Please re-run scripts/dev/install.sh or recreate venv using Python ${PYTHON_VERSION} manually by running (scripts/dev/recreate_python_venv.sh).${NO_COLOR}"
  echo "which python: $(which python)"
  echo "python --version:"
  python --version
  exit 1
fi

python "$@"
