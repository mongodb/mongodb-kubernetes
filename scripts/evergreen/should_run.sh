#!/usr/bin/env bash

# This script determines if a CI scope should run based on changed files in the PR.
# Exit code 0 means the scope should run, exit code != 0 means it should be skipped.
#
# Usage:
#   should_run.sh <scope> [base_commit]
#
# Arguments:
#   scope       - One of: e2e_tests, unit_tests, build_images
#   base_commit  - Git commit/branch to compare against (default: origin/master)
#
# Examples:
#   should_run.sh e2e_tests
#   should_run.sh unit_tests origin/master
#   should_run.sh build_images HEAD~1

set -Eeou pipefail

# Check arguments
SCOPE="${1:-}"
BASE_COMMIT="${2:-origin/master}"

if [[ -z "${SCOPE}" ]]; then
  echo "Error: scope argument is required" >&2
  echo "Usage: should_run.sh <scope> [base_commit]" >&2
  echo "  scope: e2e_tests, unit_tests, or build_images" >&2
  exit 1
fi

# Determine project directory (try multiple methods)
# In Evergreen, PROJECT_DIR or workdir will be set
# Locally, we detect from script location
PROJECT_DIR="${PROJECT_DIR:-${workdir:-}}"
if [[ -z "${PROJECT_DIR}" ]]; then
  # Try to detect from script location
  SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  PROJECT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
fi
if [[ -z "${PROJECT_DIR}" ]] || [[ ! -d "${PROJECT_DIR}" ]]; then
  # Fallback to current directory
  PROJECT_DIR="$(pwd)"
fi

# Path to config file (relative to project root)
CONFIG_FILE="${PROJECT_DIR}/.evergreen-path-filter.json"

if [[ ! -f "${CONFIG_FILE}" ]]; then
  echo "Error: Config file not found: ${CONFIG_FILE}" >&2
  exit 1
fi

# Check if jq is available
if ! command -v jq >/dev/null 2>&1; then
  # Try to find jq in common locations
  if [[ -f "${PROJECT_DIR:-.}/bin/jq" ]]; then
    export PATH="${PROJECT_DIR:-.}/bin:${PATH}"
  elif [[ -f "${workdir:-}/bin/jq" ]]; then
    export PATH="${workdir:-}/bin:${PATH}"
  else
    echo "Error: jq is required but not found. Please ensure jq is installed or setup_jq has run." >&2
    exit 1
  fi
fi

# Validate scope exists in config
if ! jq -e ".scopes.${SCOPE}" "${CONFIG_FILE}" >/dev/null 2>&1; then
  echo "Error: Unknown scope '${SCOPE}'. Available scopes:" >&2
  jq -r '.scopes | keys[]' "${CONFIG_FILE}" | sed 's/^/  - /' >&2
  exit 1
fi

echo "Checking if '${SCOPE}' should run (comparing against ${BASE_COMMIT})"

# Fetch to ensure we have the latest refs (non-fatal if it fails)
git fetch origin --quiet 2>/dev/null || true

# Check if we're already on the base branch (merged code should always run)
CURRENT_BRANCH=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "")
BASE_BRANCH_NAME="${BASE_COMMIT#origin/}"

# If BASE_COMMIT is a branch reference (starts with origin/), check if we're on that branch
if [[ "${BASE_COMMIT}" == origin/* ]] && [[ "${CURRENT_BRANCH}" == "${BASE_BRANCH_NAME}" ]]; then
  DESCRIPTION=$(jq -r ".scopes.${SCOPE}.description" "${CONFIG_FILE}")
  echo "${DESCRIPTION}: Always run (on ${BASE_BRANCH_NAME} branch - merged code)."
  exit 0
fi

# Get list of changed files
CHANGED_FILES=$(git diff --name-only "${BASE_COMMIT}"...HEAD 2>/dev/null || git diff --name-only "${BASE_COMMIT}" HEAD 2>/dev/null || echo "")

# If no changed files, check if we're on the base branch (merged code should always run)
if [[ -z "${CHANGED_FILES}" ]]; then
  # Check if HEAD matches BASE_COMMIT (we're on the base branch)
  if git diff --quiet "${BASE_COMMIT}" HEAD 2>/dev/null; then
    # If BASE_COMMIT is a branch reference, always run (merged code)
    if [[ "${BASE_COMMIT}" == origin/* ]]; then
      DESCRIPTION=$(jq -r ".scopes.${SCOPE}.description" "${CONFIG_FILE}")
      echo "${DESCRIPTION}: Always run (HEAD matches ${BASE_COMMIT} - merged code)."
      exit 0
    fi
  fi
  echo "No changed files detected. Skipping ${SCOPE}."
  exit 1
fi

echo "Changed files:"
echo "${CHANGED_FILES}" | sed 's/^/  - /'

# Get trigger patterns from config
TRIGGER_PATTERNS=$(jq -r ".scopes.${SCOPE}.trigger_patterns[]" "${CONFIG_FILE}")

# Check if any file matches trigger patterns
HAS_TRIGGER_MATCH=false
while IFS= read -r file; do
  while IFS= read -r pattern; do
    if [[ "${file}" =~ ${pattern} ]]; then
      HAS_TRIGGER_MATCH=true
      echo "  → Matches trigger pattern: ${pattern}"
      break
    fi
  done <<< "${TRIGGER_PATTERNS}"
  if [[ "${HAS_TRIGGER_MATCH}" == true ]]; then
    break
  fi
done <<< "${CHANGED_FILES}"

if [[ "${HAS_TRIGGER_MATCH}" == true ]]; then
  DESCRIPTION=$(jq -r ".scopes.${SCOPE}.description" "${CONFIG_FILE}")
  echo "${DESCRIPTION}: Should run (trigger pattern matched)."
  exit 0
fi

# No trigger patterns matched - skip
DESCRIPTION=$(jq -r ".scopes.${SCOPE}.description" "${CONFIG_FILE}")
echo "${DESCRIPTION}: Skipping (no trigger patterns matched)."
exit 1
