#!/usr/bin/env bash

# Instead of calling the publish_helm_chart.py directly from .evergreen-functions.yaml
# we are calling that via this .sh so that we can easily pass build_scenario from env var that
# is set via context files. Using the env vars, set via context files, in .evergreen configuration
# is not that straightforward.
set -Eeou pipefail

source scripts/dev/set_env_context.sh

if [[ -n "${tag_description_override:-}" ]]; then
  annotation="${tag_description_override}"
else
  annotation=$(git tag -l --format='%(contents:subject)' "${triggered_by_git_tag:-}" 2>/dev/null || true)
fi

registry_override=""
if echo "${annotation}" | grep -qF '[dry-run]'; then
  if [[ -z "${dryrun_registry_override:-}" ]]; then
    echo "ERROR: dry-run detected but dryrun_registry_override is unset" >&2
    exit 1
  fi
  registry_override="${dryrun_registry_override}"

  # Re-login with staging credentials (helm_registry_login_prod already ran in the task,
  # but we need staging access to push to quay.io/mongodb/staging).
  echo "Dry-run: re-logging into helm registry with staging credentials"
  QUAY_USERNAME="${quay_staging_username}" QUAY_PASSWORD="${quay_staging_robot_token}" \
    scripts/dev/run_python.sh scripts/release/helm_registry_login.py --build_scenario "${BUILD_SCENARIO}"
fi

scripts/dev/run_python.sh scripts/release/publish_helm_chart.py --build-scenario "${BUILD_SCENARIO}" --version "${OPERATOR_VERSION}" --registry-override "${registry_override}"
