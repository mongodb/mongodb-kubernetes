#!/usr/bin/env bash
set -Eeou pipefail

# Store the current state of the index and working directory
initial_index_state=$(git diff --name-only --cached --diff-filter=AM)

export EVERGREEN_MODE=true

.githooks/pre-commit
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

  echo "We have files that differ after running pre-commit, please run the pre-commit and precommit-with-licenses locally"
  echo "Full diff: "
  git diff --cached --diff-filter=AM
  echo "The following files differ: "
  git diff --name-only --cached --diff-filter=AM
  exit 1
else
  echo "No changes detected, clean state"
  exit 0
fi
