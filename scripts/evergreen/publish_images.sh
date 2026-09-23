#!/usr/bin/env bash
set -euo pipefail
# Extracted from .evergreen-release-publish.yml inlined shell.exec because the
# YAML expansion parser mangled ${...} inside heredocs.

source .generated/context.export.env

# tag_description_override is a patch param; real tags use the actual git annotation.
if [[ -n "${tag_description_override:-}" ]]; then
  annotation="${tag_description_override}"
else
  annotation=$(git tag -l --format='%(contents:subject)' "${triggered_by_git_tag:-}" 2>/dev/null || true)
fi

if echo "${annotation}" | grep -qF '[dry-run]'; then
  IS_DRYRUN=true
else
  IS_DRYRUN=false
fi

registry_override=""
if [[ "${IS_DRYRUN}" == "true" ]]; then
  if [[ -z "${dryrun_registry_override:-}" ]]; then
    echo "ERROR: IS_DRYRUN=true but dryrun_registry_override is unset" >&2
    exit 1
  fi
  registry_override="${dryrun_registry_override}"
fi

release_commit="${target_commit:-${revision}}"
echo "TRACE: tag=${triggered_by_git_tag:-} IS_DRYRUN=${IS_DRYRUN} commit=${release_commit}"
scripts/mckci release publish images --commit "${release_commit}" --latest-marker latest --registry-override "${registry_override}"

