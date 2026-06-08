#!/usr/bin/env bash

set -Eeou pipefail
source scripts/dev/set_env_context.sh

export GOLANGCI_LINT_CACHE="${HOME}/.cache/golangci-lint"

ORIGINAL_BRANCH=""
# Detect the original branch (same commit, but not the evg-pr-test-* branch which evg creates)
ORIGINAL_BRANCH=$(git for-each-ref --format='%(refname:short) %(objectname)' refs/remotes/origin | grep "$(git rev-parse HEAD)" | grep -v "evg-pr-test-" | awk '{print $1}' | sed 's|^origin/||' | head -n 1 || true)

if [[ -z "${ORIGINAL_BRANCH}" ]]; then
  echo "Fork: Could not determine the original branch. Running in a fork"
  exit 0
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
    echo "✅ Match found: '${ORIGINAL_BRANCH}' matches pattern '${pattern}'"
    break
  fi
done

if [[ "${MATCH_FOUND}" == false ]]; then
  echo "❌ Branch '${ORIGINAL_BRANCH}' does not match any required patterns. Exiting."
  printf " - %s\n" "${REQUIRED_PATTERNS[@]}"
  exit 0
fi

echo "Detected a branch that should be bumped."

git checkout "${ORIGINAL_BRANCH}"

# Run pre-commit - allow failure (exit 1 means files were modified)
EVERGREEN_MODE=true .githooks/pre-commit || true

git add .

if [[ -z $(git diff --name-only --cached) ]]; then
  echo "No staged changes to commit. Exiting."
  exit 0
fi

git commit -m "Run pre-commit hook"
git remote set-url origin https://x-access-token:"${GH_TOKEN}"@github.com/mongodb/mongodb-kubernetes.git

echo "changes detected, pushing them"
git push origin "${ORIGINAL_BRANCH}"
