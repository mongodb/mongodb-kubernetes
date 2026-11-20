#!/usr/bin/env bash
# Commits the Search third-party image bump and opens a pull request, or updates the open one.
# Called by .github/workflows/bump-third-party-images.yml after bump_third_party_images.py changed files.
#
# Environment:
#   GH_TOKEN            Token used by gh.
#   GITHUB_REF_NAME     Base branch (default: master).

set -euo pipefail

branch="auto/bump-search-third-party-images"
title="Bump Search third-party images"
label="evergreen:e2e-search-images"
base="${GITHUB_REF_NAME:-master}"
# This run's changelog is the only untracked file in changelog/.
changes="$(git ls-files --others --exclude-standard -- changelog | xargs grep -h '^\* ')"
body="Automated bump of the third-party images used by MongoDBSearch (Envoy and the OpenTelemetry collector) to the newest tags. A new minor version must be at least 7 days old; patches of an adopted minor are taken immediately.

${changes}

See release.json and the changelog entry for details. The e2e tests run through the ${label} label.

This PR is opened with GITHUB_TOKEN, so pull_request-triggered GitHub checks may not start; close and reopen the PR, or push to it, to trigger them."

pr="$(gh pr list --head "${branch}" --state open --json number --jq '.[0].number // empty')"

# Leave an open bump PR alone (it may carry human fixes) when it already has the same versions.
if [[ -n "${pr}" ]] && git fetch --depth=1 origin "${branch}" 2>/dev/null && git diff --quiet FETCH_HEAD -- release.json; then
  echo "bump PR already up to date"
  exit 0
fi

git config user.name "github-actions[bot]"
git config user.email "41898282+github-actions[bot]@users.noreply.github.com"
git checkout -B "${branch}"
git add release.json helm_chart config public changelog
git commit -m "${title}"
# Force-push: the auto/ namespace is owned by this automation, and this refreshes an already open PR.
git push --force origin HEAD

if [[ -n "${pr}" ]]; then
  gh pr edit "${pr}" --body "${body}"
else
  gh pr create --base "${base}" --head "${branch}" --title "${title}" --body "${body}" --label "${label}"
fi
