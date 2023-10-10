#!/usr/bin/env bash

set -Eeou pipefail

source scripts/dev/set_env_context.sh

# shellcheck disable=SC2154
"${workdir}"/venv/bin/python  pipeline.py "$@"