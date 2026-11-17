#!/usr/bin/env bash
# Opens a pull request for a third-party image bump after scripts/dev/bump_third_party_images.py has updated
# release.json and the generated files have been regenerated. Modeled on create-go-bump-pr.sh.
# Called by .github/workflows/bump-third-party-images.yml; can also be run standalone.
#
# Usage: create-third-party-images-bump-pr.sh <branch-suffix> <title> <body-file>
#   <branch-suffix>  e.g. otel-0.165.1 or otel-0.165.1-envoy-v1.37.12; the branch is
#                    auto/bump-third-party-images-<branch-suffix>
#   <title>          pull request title and commit message
#   <body-file>      file with the pull request description
#
# Only the files a bump is expected to touch are committed; any other modification fails the run.
#
# Environment:
#   TEST_BUMP_DRY_RUN=1   Print what would happen without touching git or gh.
#   GIT_AUTHOR_NAME       Override committer name  (default: github-actions[bot])
#   GIT_AUTHOR_EMAIL      Override committer email (default: github-actions[bot] noreply)

set -euo pipefail

if [[ $# -ne 3 || -z "${1}" || -z "${2}" || -z "${3}" ]]; then
  echo "usage: create-third-party-images-bump-pr.sh <branch-suffix> <title> <body-file>" >&2
  echo "  example: create-third-party-images-bump-pr.sh otel-0.165.1 'Bump otel image to 0.165.1' body.md" >&2
  exit 1
fi

branch="auto/bump-third-party-images-${1}"
title="${2}"
body_file="${3}"
if [ -n "${GITHUB_REF_NAME:-}" ]; then
  base_branch="${GITHUB_REF_NAME}"
else
  base_branch="$(git rev-parse --abbrev-ref HEAD)"
fi

if [[ "${TEST_BUMP_DRY_RUN:-}" == "1" ]]; then
  printf 'create-third-party-images-bump-pr: dry-run: would open PR "%s" from branch %s into %s\n' "${title}" "${branch}" "${base_branch}"
  exit 0
fi

[[ -f "${body_file}" ]] || {
  echo "create-third-party-images-bump-pr: error: body file ${body_file} not found" >&2
  exit 1
}

command -v gh >/dev/null 2>&1 || {
  echo "create-third-party-images-bump-pr: error: gh is required" >&2
  exit 1
}

# Everything a bump may change: the source of truth, its derived files and the changelog entry.
allowed_paths=(
  release.json
  helm_chart/values.yaml
  public/mongodb-kubernetes.yaml
  public/mongodb-kubernetes-openshift.yaml
  public/mongodb-kubernetes-multi-cluster.yaml
  config/manager/manager.yaml
  changelog
)

unexpected="$(git status --porcelain --untracked-files=all | awk '{print $2}' | grep -v -E "^($(IFS='|'; echo "${allowed_paths[*]}"))(/|$)" || true)"
if [[ -n "${unexpected}" ]]; then
  echo "create-third-party-images-bump-pr: error: unexpected modified files, refusing to commit:" >&2
  echo "${unexpected}" >&2
  exit 1
fi

if [[ -n "$(gh pr list --head "${branch}" --state open --json number --jq '.[].number')" ]]; then
  echo "create-third-party-images-bump-pr: a pull request from ${branch} is already open, nothing to do"
  exit 0
fi

git config user.name  "${GIT_AUTHOR_NAME:-github-actions[bot]}"
git config user.email "${GIT_AUTHOR_EMAIL:-41898282+github-actions[bot]@users.noreply.github.com}"

git checkout -b "${branch}"
git add -- "${allowed_paths[@]}"
git commit -m "${title}"
# Force-push: the auto/bump-third-party-images-* namespace is owned by this automation.
# A stale remote branch can linger if a prior PR was closed without merging;
# overwriting it is safe and lets retries succeed.
git push --force origin "${branch}"

gh pr create \
  --title "${title}" \
  --body-file "${body_file}" \
  --base "${base_branch}" \
  --head "${branch}"
