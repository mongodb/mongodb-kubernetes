#!/usr/bin/env bash

set -Eeou pipefail

source scripts/dev/set_env_context.sh

scripts/dev/run_python.sh scripts/release/pipeline.py "${IMAGE_NAME}" --build-scenario "${BUILD_SCENARIO}" --version "${OPERATOR_VERSION}"
