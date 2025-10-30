#!/usr/bin/env bash

# Instead of calling the publish_helm_chart.py directly from .evergreen-functions.yaml
# we are calling that via this .sh so that we can easily pass build_scenario from env var that
# is set via context files. Using the env vars, set via context files, in .evergreen configuration
# is not that straightforward.
set -Eeou pipefail

source scripts/dev/set_env_context.sh

scripts/dev/run_python.sh scripts/release/publish_helm_chart.py --build-scenario "${BUILD_SCENARIO}" --version "${OPERATOR_VERSION}"
