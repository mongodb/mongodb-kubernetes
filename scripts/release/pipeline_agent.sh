#!/usr/bin/env bash

set -Eeou pipefail

source scripts/dev/set_env_context.sh

if [[ "${FLAGS:-}" = "" ]]; then
    FLAGS="--parallel"
else
    FLAGS="--parallel ${FLAGS}"
fi

# shellcheck disable=SC2086
scripts/dev/run_python.sh scripts/release/pipeline.py agent \
    --build-scenario "${BUILD_SCENARIO}" \
    ${FLAGS}
