#!/usr/bin/env bash

set -Eeou pipefail

source scripts/dev/set_env_context.sh

scripts/dev/run_python.sh scripts/release/pipeline.py ops-manager \
    --build-scenario "${BUILD_SCENARIO}" \
    --version "${OM_VERSION}"
