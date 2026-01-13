#!/usr/bin/env bash
#
# CI script to run pre-commit checks in Evergreen.
# This script is called by the Evergreen CI pipeline.
#

set -Eeou pipefail

source scripts/dev/set_env_context.sh
source scripts/funcs/printing

# Activate virtual environment if it exists
if [ -f "${PROJECT_DIR}/venv/bin/activate" ]; then
  source "${PROJECT_DIR}/venv/bin/activate"
fi

# Ensure pre-commit is installed
if ! command -v pre-commit &>/dev/null; then
  echo "pre-commit not found, installing..."
  pip install pre-commit
fi

title "Running pre-commit checks"

# Set EVERGREEN_MODE to signal we're in CI
export EVERGREEN_MODE=true

# Store the current state of the index and working directory
initial_index_state=$(git diff --name-only --cached --diff-filter=AM)

# Run pre-commit on all files
# --show-diff-on-failure shows what changes hooks would make
pre-commit run --all-files --show-diff-on-failure --verbose
echo "Pre-commit hook has completed."

# Stage any changes made by the pre-commit hook
git add -u

# Store the new state of the index and working directory
final_index_state=$(git diff --name-only --cached --diff-filter=AM)

# Check if there are differences between the initial and final states
if [ "${initial_index_state}" != "${final_index_state}" ]; then
  echo "Initial index state:"
  echo "${initial_index_state}"

  echo "Final index state:"
  echo "${final_index_state}"

  echo "We have files that differ after running pre-commit, please run make precommit locally"
  echo "Full diff: "
  git diff --cached --diff-filter=AM
  echo "The following files differ: "
  git diff --name-only --cached --diff-filter=AM
  exit 1
else
  echo "No changes detected, clean state"
  exit 0
fi
