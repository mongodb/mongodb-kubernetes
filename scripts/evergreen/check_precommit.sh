#!/usr/bin/env bash
set -Eeou pipefail

# Store the current state of the index and working directory
initial_index_state=$(git diff --name-only --cached --diff-filter=AM)

export EVERGREEN_MODE=true
# CI always runs with license updates enabled
export MDB_UPDATE_LICENSES=true

# shellcheck disable=SC1091
source scripts/dev/set_env_context.sh

if [[ -f "${PROJECT_DIR}/venv/bin/activate" ]]; then
  echo "Activating venv..."
  # shellcheck disable=SC1091
  source "${PROJECT_DIR}/venv/bin/activate"
fi

echo "Running pre-commit hooks..."
echo "pre-commit version: $(pre-commit --version)"

# Run pre-commit with verbose output
# --show-diff-on-failure shows what changed when hooks modify files
pre-commit run --all-files --show-diff-on-failure --verbose

echo "Pre-commit hook has completed successfully."

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

  echo "We have files that differ after running pre-commit, please run make precommit-with-licenses locally"
  echo "Full diff: "
  git diff --cached --diff-filter=AM
  echo "The following files differ: "
  git diff --name-only --cached --diff-filter=AM
  exit 1
else
  echo "No changes detected, clean state"
  exit 0
fi
