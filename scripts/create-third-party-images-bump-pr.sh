#!/usr/bin/env bash
# Commits the Search third-party image bump and opens a pull request, or updates the open one.
# Skips the push when the open bump PR has the same image versions, and refuses to overwrite non-bot commits or touch
# a bump PR targeting another base.
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

image_versions() { jq -c '.search | {envoyVersion, metricsForwarderVersion}'; }

bot_name="github-actions[bot]"
bot_email="41898282+github-actions[bot]@users.noreply.github.com"

# Only a same-repo PR counts (fork PRs are ignored); a PR retargeted to another base is found and left alone.
pr_info="$(gh pr list --head "${branch}" --state open --json number,isCrossRepository,baseRefName \
  --jq '[.[] | select(.isCrossRepository | not)][0] | select(.) | "\(.number) \(.baseRefName)"')"
read -r pr pr_base <<<"${pr_info}"

same_versions=false
if [[ -n "${pr}" ]]; then
  if [[ "${pr_base}" != "${base}" ]]; then
    echo "bump PR #${pr} targets ${pr_base}, not ${base}; not touching it" >&2
    exit 1
  fi
  # A failed fetch must fail the script rather than fall through to a force-push.
  git fetch --depth=1 origin "${branch}"
  tip="$(git rev-parse FETCH_HEAD)"
  if [[ "$(git show "${tip}:release.json" | image_versions)" == "$(image_versions < release.json)" ]]; then
    same_versions=true
  elif [[ "$(git log -1 --format='%ae %ce' "${tip}")" != "${bot_email} ${bot_email}" ]]; then
    echo "bump PR #${pr} has commits not made by this automation; not overwriting them" >&2
    exit 1
  fi
else
  # No open PR: a stale branch from a closed or merged PR may be overwritten. Empty if the branch does not exist.
  tip="$(git ls-remote origin "refs/heads/${branch}" | cut -f1)"
fi

if [[ "${same_versions}" == false ]]; then
  git config user.name "${bot_name}"
  git config user.email "${bot_email}"
  git checkout -B "${branch}"
  git add release.json helm_chart config public changelog
  git commit -m "${title}"
  # The lease preserves a push made during this run; an empty tip requires that the branch does not exist.
  git push --force-with-lease="${branch}:${tip}" origin HEAD
fi

# Also refreshes the description and label if a previous run pushed but failed to update them.
if [[ -n "${pr}" ]]; then
  gh pr edit "${pr}" --body "${body}" --add-label "${label}"
else
  gh pr create --base "${base}" --head "${branch}" --title "${title}" --body "${body}" --label "${label}"
fi
