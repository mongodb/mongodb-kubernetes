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
    # Isolated builds never skip existing tags; rebuilding every agent in release.json would be too slow.
    if [[ "${ISOLATED_IMAGE_NAMESPACE:-}" != "" && "${IMAGE_VERSION}" == "all" ]]; then
      IMAGE_VERSION="current"
    fi
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
if [[ "${IMAGE_NAME}" == "agent" && "${AGENT_TOOLS_VERSION:-}" != "" ]]; then
    args+=(--agent-tools-version "${AGENT_TOOLS_VERSION}")
fi

# Pass custom agent URL if set (for testing custom agent builds)
if [[ "${MDB_CUSTOM_AGENT_URL:-}" != "" ]]; then
    args+=(--custom-agent-url "${MDB_CUSTOM_AGENT_URL}")
fi

# Agent and Ops Manager images are tagged by their own version and reused across patches and branches (skip-if-exists),
# so Dockerfile changes are only built and consumed in isolation (see scripts/dev/contexts/isolated-image-namespace.sh).
if [[ "${ISOLATED_IMAGE_NAMESPACE:-}" != "" && ( "${IMAGE_NAME}" == "agent" || "${IMAGE_NAME}" == "ops-manager" ) ]]; then
    args+=(--isolated-namespace "${ISOLATED_IMAGE_NAMESPACE}")
fi

if [[ "${FLAGS:-}" != "" ]]; then
    IFS=" " read -ra flags <<< "${FLAGS}"
    args+=("${flags[@]}")
fi

scripts/dev/run_python.sh scripts/release/pipeline.py "${args[@]}"
