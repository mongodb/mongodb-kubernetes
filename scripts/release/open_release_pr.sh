#!/usr/bin/env bash
# Runs `make precommit-full`, commits any regenerated files, pushes the current
# branch, and opens a PR against master. Intended to be invoked after
# `release.json` has been bumped and `copy_release_dockerfiles.py` has run.
#
# Usage: scripts/release/open_release_pr.sh <version> [--draft] [--dry-run]
#   <version>  expected mongodbOperator value; asserted against release.json
#   --draft    open the PR as draft
#   --dry-run  print actions without executing them

set -euo pipefail

DRAFT=0
DRY_RUN=0
VERSION=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --draft)   DRAFT=1; shift ;;
    --dry-run) DRY_RUN=1; shift ;;
    -*)        echo "unknown flag: $1" >&2; exit 1 ;;
    *)
      if [[ -n "${VERSION}" ]]; then
        echo "unexpected extra argument: $1" >&2; exit 1
      fi
      VERSION="$1"; shift ;;
  esac
done

if [[ -z "${VERSION}" ]]; then
  echo "usage: open_release_pr.sh <version> [--draft] [--dry-run]" >&2
  exit 1
fi

BRANCH="$(git symbolic-ref --short HEAD)"
if [[ "${BRANCH}" == "master" ]]; then
  echo "error: cannot open a release PR from master" >&2
  exit 1
fi
if [[ "${BRANCH}" == release-* ]]; then
  # release-* is GitHub-protected in this repo; pushes/merges to it fail.
  echo "error: branch '${BRANCH}' matches protected pattern 'release-*'" >&2
  echo "       rename the branch before re-running" >&2
  exit 1
fi

if ! git diff --quiet || ! git diff --cached --quiet; then
  echo "error: working tree has uncommitted changes" >&2
  echo "       commit or stash before running this script" >&2
  exit 1
fi

got=$(python3 -c "import json; print(json.load(open('release.json'))['mongodbOperator'])")
if [[ "${got}" != "${VERSION}" ]]; then
  echo "error: release.json mongodbOperator is '${got}', expected '${VERSION}'" >&2
  echo "       bump + copy first, or pass the actual version" >&2
  exit 1
fi

run() {
  echo "→ $*"
  if [[ "${DRY_RUN}" -eq 0 ]]; then
    "$@"
  fi
}

run make precommit-full

if [[ "${DRY_RUN}" -eq 0 ]]; then
  if git diff --quiet && git diff --cached --quiet; then
    echo "→ no files changed by precommit-full; skipping commit"
  else
    run git add -A
    run git commit -m "Regenerate release artifacts for ${VERSION}"
  fi
else
  echo "→ [dry-run] would git add -A && git commit (if precommit produced changes)"
fi

run git push -u origin "${BRANCH}"

TITLE="Release MCK ${VERSION}"
BODY="## Summary

Release PR for MCK ${VERSION}.

Opened via \`scripts/release/open_release_pr.sh\`.

## Included changes

- Bump \`mongodbOperator\` to ${VERSION}
- Copy release Dockerfiles to \`public/dockerfiles/*/${VERSION}/ubi/\`
- Regenerate release artifacts (Helm chart, manifests, CSV, licenses, RBAC)

## Checklist

- [ ] CI green
- [ ] \`release.json\` \`mongodbOperator\` = ${VERSION}
- [ ] Expected Dockerfiles present under \`public/dockerfiles/*/${VERSION}/\`"

draft_args=()
[[ "${DRAFT}" -eq 1 ]] && draft_args+=(--draft)

run gh pr create "${draft_args[@]}" --base master --title "${TITLE}" --body "${BODY}" --head "${BRANCH}"
