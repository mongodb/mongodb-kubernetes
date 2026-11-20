#!/usr/bin/env bash

# Instead of calling the publish_helm_chart.py directly from .evergreen-functions.yaml
# we are calling that via this .sh so that we can easily pass build_scenario from env var that
# is set via context files. Using the env vars, set via context files, in .evergreen configuration
# is not that straightforward.
set -Eeou pipefail

source scripts/dev/set_env_context.sh

# Empty unless dryrun-release; in that case REGISTRY is the required staging destination.
registry_override=""
if [[ "${BUILD_SCENARIO:-}" == "dryrun-release" ]]; then
  registry_override="${REGISTRY:?REGISTRY must be set when BUILD_SCENARIO is dryrun-release}"
fi

# Helm registry login. Prod and staging charts live on the same host (quay.io)
# and a helm login is per-host, so exactly ONE login happens here (prod robot for
# real releases, staging robot for dry-run releases via the REGISTRY override).
QUAY_USERNAME="${quay_username:?quay_username must be set}" \
  QUAY_PASSWORD="${quay_password:?quay_password must be set}" \
  scripts/dev/run_python.sh scripts/release/helm_registry_login.py --build_scenario "${BUILD_SCENARIO}"

scripts/dev/run_python.sh scripts/release/publish_helm_chart.py --build-scenario "${BUILD_SCENARIO}" --version "${OPERATOR_VERSION}" --registry-override "${registry_override}"
