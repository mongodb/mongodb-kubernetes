#!/usr/bin/env bash
set -Eeou pipefail

# shellcheck disable=SC2317
if [ -n "$(git diff --name-only --cached --diff-filter=AM)" ]; then
  echo "We have a dirty state, probably a patch, skipping check_precommit"
  echo "full diff is"
  git diff --cached --diff-filter=AM
  exit 0
fi

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
if [ "$initial_index_state" != "$final_index_state" ]; then
  echo "Initial index state:"
  echo "$initial_index_state"

  echo "Final index state:"
  echo "$final_index_state"

  echo "We have files that differ after running pre-commit, please run the pre-commit locally"
  echo "Full diff: "
  git diff --cached --diff-filter=AM
  echo "The following files differ: "
  git diff --name-only --cached --diff-filter=AM
  exit 1
else
  echo "No changes detected, clean state"
  exit 0
fi
