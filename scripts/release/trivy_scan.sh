#!/usr/bin/env bash
#
# Scans freshly built and pushed container images for known vulnerabilities (CVEs)
# with trivy. Runs as part of the build-and-publish flows in Evergreen and resolves
# the image versions the same way scripts/release/pipeline.sh does (see
# scripts/release/trivy_scan.py).
#
# IMAGE_NAMES is a space-separated list of images; all of them are reported in a
# single message. IMAGE_NAME (a single image) is still supported.
#
# The scan is notify-only: it reports findings to the task logs and Slack
# (see scripts/release/trivy_scan.py) and never blocks the publishing pipeline.
#
# Scans only run for the "staging", "release" and "dryrun-release" build scenarios
# (build-and-publish flows). Patch/development builds are skipped unless TRIVY_SCAN_FORCE=true.

set -Eeou pipefail

source scripts/dev/set_env_context.sh

image_names="${IMAGE_NAMES:-${IMAGE_NAME:-}}"
if [[ -z "${image_names}" ]]; then
  echo "Error: IMAGE_NAMES (or IMAGE_NAME) must be set" >&2
  exit 1
fi

scenario="${BUILD_SCENARIO_OVERRIDE:-${BUILD_SCENARIO}}"

if [[ "${TRIVY_SCAN_FORCE:-false}" != "true" && "${scenario}" != "staging" && "${scenario}" != "release" && "${scenario}" != "dryrun-release" ]]; then
  echo "Skipping CVE scan of '${image_names}' for build scenario '${scenario}' (set TRIVY_SCAN_FORCE=true to override)"
  exit 0
fi

scripts/evergreen/setup_trivy.sh

IFS=" " read -ra images <<< "${image_names}"

# Ops Manager images are built per OM version in separate variants (build_om{70,80,90}_images), whose
# contexts define OM_VERSION. When scanning them from a single task, resolve all the versions from the same
# context fragments (they read the ops_manager_XX_latest anchors from .evergreen.yml).
if [[ " ${images[*]} " == *" ops-manager "* && -z "${OM_VERSIONS:-}" && -z "${OM_VERSION:-}" ]]; then
  OM_VERSIONS=""
  for om_context in om70_image om80_image om90_image; do
    OM_VERSIONS+=" $(source "scripts/dev/contexts/variables/${om_context}" && echo "${OM_VERSION}")"
  done
  export OM_VERSIONS
fi

args=("${images[@]}")
args+=(--build-scenario "${scenario}")

if [[ "${TRIVY_SCAN_FLAGS:-}" != "" ]]; then
  IFS=" " read -ra flags <<< "${TRIVY_SCAN_FLAGS}"
  args+=("${flags[@]}")
fi

scripts/dev/run_python.sh scripts/release/trivy_scan.py "${args[@]}"
