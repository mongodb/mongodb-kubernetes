#!/usr/bin/env bash

set -Eeou pipefail

source scripts/dev/set_env_context.sh

args=("${IMAGE_NAME}")
args+=(--build-scenario "${BUILD_SCENARIO_OVERRIDE:-${BUILD_SCENARIO}}")

case ${IMAGE_NAME} in
  "agent")
    # For agent: version can be "all", "current", or explicit version (requires TOOLS_VERSION)
    # AGENT_VERSION_OVERRIDE takes precedence, then AGENT_VERSION from context.
    IMAGE_VERSION="${AGENT_VERSION_OVERRIDE:-${AGENT_VERSION:-}}"
    ;;

  "ops-manager")
    IMAGE_VERSION="${OM_VERSION}"
    ;;

  "readiness-probe")
    IMAGE_VERSION="${READINESS_PROBE_VERSION}"
    ;;

  "upgrade-hook")
    IMAGE_VERSION="${VERSION_UPGRADE_HOOK_VERSION}"
    ;;

  *)
    IMAGE_VERSION="${OPERATOR_VERSION}"
    ;;
esac

if [[ "${IMAGE_VERSION:-}" != "" ]]; then
    args+=(--version "${IMAGE_VERSION}")
fi

# For agent builds, pass tools version if explicitly provided
if [[ "${IMAGE_NAME}" == "agent" && "${TOOLS_VERSION:-}" != "" ]]; then
    args+=(--agent-tools-version "${TOOLS_VERSION}")
fi

if [[ "${FLAGS:-}" != "" ]]; then
    IFS=" " read -ra flags <<< "${FLAGS}"
    args+=("${flags[@]}")
fi

scripts/dev/run_python.sh scripts/release/pipeline.py "${args[@]}"
