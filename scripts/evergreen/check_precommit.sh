#!/usr/bin/env bash
#
# Run prek checks locally (make precommit / make precommit-full) or in Evergreen CI.
#

set -Eeou pipefail

full_mode=false
if [[ "${1:-}" == "--full" ]]; then
  full_mode=true
fi

source scripts/dev/set_env_context.sh
source scripts/funcs/printing

# Ensure prek is installed
if ! command -v prek &>/dev/null; then
  echo "prek not found, please run scripts/evergreen/setup_prek.sh first" >&2
  exit 1
fi

title "Running pre-commit checks"

if [[ "${full_mode}" == true ]]; then
  export MDB_UPDATE_LICENSES=true
  export MDB_REGENERATE_RBAC=true
fi

# Store the current state of the index and working directory
initial_index_state=$(git diff --name-only --cached --diff-filter=AM)

# Run prek on all files
# --show-diff-on-failure shows what changes hooks would make
prek run --all-files --show-diff-on-failure
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
