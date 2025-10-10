#!/usr/bin/env bash

set -Eeou pipefail

source scripts/dev/set_env_context.sh

scripts/dev/run_python.sh scripts/release/pipeline.py readiness-probe \
    --build-scenario "${BUILD_SCENARIO}" \
    --version "${READINESS_PROBE_VERSION}"
