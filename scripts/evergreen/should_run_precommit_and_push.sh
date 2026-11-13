#!/usr/bin/env bash

# This file is a condition script used for conditionally executing evergreen task for running precommit and pushing (run_precommit_and_push).
# It checks if the branch matches patterns that require auto-bump functionality.
# Exit code 0 means the task should run, exit code != 0 means it should be skipped.

set -Eeou pipefail
source scripts/dev/set_env_context.sh

ORIGINAL_BRANCH=$(scripts/evergreen/detect_original_branch.sh)

if [[ -z "${ORIGINAL_BRANCH}" ]]; then
  echo "Could not determine the original branch (fork or unknown commit). Skipping precommit_and_push task."
  exit 1
fi
echo "Detected original branch: ${ORIGINAL_BRANCH}"

REQUIRED_PATTERNS=(
  "^dependabot/"
  "_version_bump$"
  "^enterprise-operator-release-"
)

echo "Checking branch '${ORIGINAL_BRANCH}' against required patterns:"

MATCH_FOUND=false
for pattern in "${REQUIRED_PATTERNS[@]}"; do
  if [[ "${ORIGINAL_BRANCH}" =~ ${pattern} ]]; then
    MATCH_FOUND=true
    echo "Match found: '${ORIGINAL_BRANCH}' matches pattern '${pattern}'"
    break
  fi
done

if [[ "${MATCH_FOUND}" == false ]]; then
  echo "Branch '${ORIGINAL_BRANCH}' does not match any required patterns. Skipping precommit_and_push task."
  printf " - %s\n" "${REQUIRED_PATTERNS[@]}"
  exit 1
fi

echo "Branch matches required patterns. Precommit and push task should run."
exit 0
