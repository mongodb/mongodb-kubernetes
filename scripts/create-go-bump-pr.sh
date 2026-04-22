#!/usr/bin/env bash
# Opens a pull request for a Go version bump after bump-go.sh has updated files.
# Called by check-go-bump-policy.sh; can also be run standalone.
#
# Usage: create-go-bump-pr.sh <version>
#   <version> is the exact go directive (e.g. 1.26.2), no "go" prefix.
#
# Environment:
#   TEST_BUMP_DRY_RUN=1   Print what would happen without touching git or gh.
#   GIT_AUTHOR_NAME       Override committer name  (default: github-actions[bot])
#   GIT_AUTHOR_EMAIL      Override committer email (default: github-actions[bot] noreply)

set -euo pipefail

if [[ $# -lt 1 || -z "${1}" ]]; then
  echo "usage: create-go-bump-pr.sh <version>" >&2
  echo "  example: create-go-bump-pr.sh 1.26.2" >&2
  exit 1
fi

version="${1#go}"
branch="auto/bump-go-${version}"
title="Bump Go version to ${version}"

if [[ "${TEST_BUMP_DRY_RUN:-}" == "1" ]]; then
  printf 'create-go-bump-pr: dry-run: would open PR "%s" from branch %s\n' "${title}" "${branch}"
  exit 0
fi

command -v gh >/dev/null 2>&1 || {
  echo "create-go-bump-pr: error: gh is required" >&2
  exit 1
}

git config user.name  "${GIT_AUTHOR_NAME:-github-actions[bot]}"
git config user.email "${GIT_AUTHOR_EMAIL:-41898282+github-actions[bot]@users.noreply.github.com}"

git checkout -b "${branch}"
git add -A
git commit -m "${title}"
git push origin "${branch}"

gh pr create \
  --title "${title}" \
  --body "$(cat <<'EOF'
## Summary

Automated Go version bump triggered by the go-bump-policy schedule.

The policy (see `scripts/check-go-bump-policy.sh`) bumps when the current
minor is within 90 days of its EOL **and** a newer stable release is
available on go.dev.

## Checklist

- [ ] CI passes
- [ ] Review propagated version in Dockerfiles, `.tool-versions`, and secondary `go.mod` files
EOF
  )" \
  --base master \
  --head "${branch}"
