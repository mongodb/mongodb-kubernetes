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

pyenv shell "${required_version}"

python "$@"
