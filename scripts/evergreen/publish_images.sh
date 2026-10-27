#!/usr/bin/env bash
set -euo pipefail
# Extracted from .evergreen-release-publish.yml inlined shell.exec because the
# YAML expansion parser mangled ${...} inside heredocs.

source .generated/context.export.env

# Login to quay.io with the normalized credentials from the Evergreen context
# (prod robot for real publishes, staging robot for dry runs).
echo "${quay_password:?quay_password must be set}" | \
  docker login --username "${quay_username:?quay_username must be set}" --password-stdin quay.io

registry_override=""
if [[ "${BUILD_SCENARIO:-}" == "dryrun-release" ]]; then
  registry_override="${REGISTRY:?REGISTRY must be set when BUILD_SCENARIO is dryrun-release}"
fi

release_commit="${target_commit:-${revision}}"
echo "TRACE: tag=${triggered_by_git_tag:-} commit=${release_commit}"
scripts/mckci release publish images --commit "${release_commit}" --latest-marker latest --registry-override "${registry_override}"

