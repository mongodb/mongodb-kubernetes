#!/usr/bin/env bash
#
# Scans a freshly built and pushed container image for known vulnerabilities (CVEs)
# with trivy. Runs as part of the build-and-publish tasks in Evergreen, right after
# scripts/release/pipeline.sh, and resolves the image version the same way.
#
# The scan is notify-only: it reports findings to the task logs and Slack
# (see scripts/release/trivy_scan.py) and never blocks the publishing pipeline.
#
# Scans only run for the "staging" and "release" build scenarios (build-and-publish
# flows). Patch/development builds are skipped unless TRIVY_SCAN_FORCE=true.

set -Eeou pipefail

source scripts/dev/set_env_context.sh

scenario="${BUILD_SCENARIO_OVERRIDE:-${BUILD_SCENARIO}}"

if [[ "${TRIVY_SCAN_FORCE:-false}" != "true" && "${scenario}" != "staging" && "${scenario}" != "release" ]]; then
  echo "Skipping CVE scan of '${IMAGE_NAME}' for build scenario '${scenario}' (set TRIVY_SCAN_FORCE=true to override)"
  exit 0
fi

# Dry-run releases (release_publish variant with [dry-run] in the tag annotation)
# publish to an override registry, so the production image refs we would scan
# do not correspond to the published artifacts.
if [[ "${IS_DRYRUN:-false}" == "true" ]]; then
  echo "Skipping CVE scan of '${IMAGE_NAME}': IS_DRYRUN=true (dry-run release)"
  exit 0
fi

scripts/evergreen/setup_trivy.sh

args=("${IMAGE_NAME}")
args+=(--build-scenario "${scenario}")

# Resolve the image version exactly like scripts/release/pipeline.sh does, so we
# scan the same tag that was just built and pushed.
case ${IMAGE_NAME} in
  "agent")
    # "all"/"current" are expanded to the currently used agent versions by trivy_scan.py
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

args+=(--version "${IMAGE_VERSION}")

if [[ "${TRIVY_SCAN_FLAGS:-}" != "" ]]; then
  IFS=" " read -ra flags <<< "${TRIVY_SCAN_FLAGS}"
  args+=("${flags[@]}")
fi

scripts/dev/run_python.sh scripts/release/trivy_scan.py "${args[@]}"
