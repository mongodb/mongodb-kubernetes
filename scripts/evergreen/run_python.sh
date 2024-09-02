#!/usr/bin/env bash

set -Eeou pipefail

source scripts/dev/set_env_context.sh

# shellcheck disable=SC2154
if [ -f "${workdir}"/venv/bin/activate ]; then
    source "${workdir}"/venv/bin/activate
fi

export PYTHONPATH="$workdir"

python "$@"
