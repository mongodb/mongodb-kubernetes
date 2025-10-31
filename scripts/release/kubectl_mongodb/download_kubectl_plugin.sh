#!/usr/bin/env bash

set -Eeou pipefail

source scripts/dev/set_env_context.sh

scripts/dev/run_python.sh scripts/release/kubectl_mongodb/download_kubectl_plugin.py --build-scenario "${BUILD_SCENARIO}" --version "${OPERATOR_VERSION}" --platform "${PLATFORM}"
