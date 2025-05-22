#!/usr/bin/env bash

set -eou pipefail

release_branch="$1"
if [[ -z "${release_branch}" ]]; then
  echo "Usage: $0 <release-branch>"
  exit 1
fi
current_date=$(date "+%Y%m%d")
branch_pattern="origin/meko-snippets-update-${current_date}*"
new_branch_name="mck-snippets-update-cherry-picked-${current_date}-$(date "+%H%M%S")"

echo "Fetching from origin..."
git fetch origin

echo "Looking for branches matching '${branch_pattern}'..."
branches_to_pick=()
while IFS= read -r line; do
    branches_to_pick+=("$line")
done < <(git branch -r --list "${branch_pattern}" | sed 's/^[[:space:]]*//')

if [[ ${#branches_to_pick[@]} -eq 0 ]]; then
  echo "No branches found matching '${branch_pattern}'. Exiting."
  exit 0
fi

echo "Found branches to cherry-pick:"
printf "  %s\n" "${branches_to_pick[@]}"

echo "Creating new branch '${new_branch_name}' from 'origin/${release_branch}'..."
git checkout -b "${new_branch_name}" "origin/${release_branch}"

echo "Cherry-picking commits..."
for branch in "${branches_to_pick[@]}"; do
  echo "  Picking from ${branch}..."
  git cherry-pick "${branch}" || true

  # automatically resolve cherry-pick conflicts by taking whatever it is in the cherry-picked commit
  # in case there are no conflicts, this will do nothing
  git status --porcelain | grep "^UU" | awk '{print $2}' | xargs -I {} git checkout --theirs {} || true
  git status --porcelain | grep "^UU" | awk '{print $2}' | xargs -I {} git add {} || true
  # || true for ignoring errors if there are no conflicts
  git cherry-pick --continue || true
done

echo "New branch '${new_branch_name}' created with cherry-picked commits."
echo "Push with: git push origin ${new_branch_name}"
