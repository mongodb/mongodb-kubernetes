#!/usr/bin/env bash

set -Eeou pipefail

# Instead of calling the publish_helm_chart.py directly from .evergreen-functions.yaml
# we are calling that via this .sh so that we can easily pass build_scenario from env var that
# is set via context files. Using the env vars, set via context files, in .evergreen configuraiton 
# is not hat straightforward.
source scripts/dev/set_env_context.sh

scripts/dev/run_python.sh scripts/release/helm_registry_login.py --build_scenario "${BUILD_SCENARIO}"
