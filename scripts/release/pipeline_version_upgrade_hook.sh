#!/usr/bin/env bash

set -Eeou pipefail

source scripts/dev/set_env_context.sh

scripts/dev/run_python.sh scripts/release/pipeline.py upgrade-hook \
    --build-scenario "${BUILD_SCENARIO}" \
    --version "${VERSION_UPGRADE_HOOK_VERSION}"
