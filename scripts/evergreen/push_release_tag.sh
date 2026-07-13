#!/usr/bin/env bash

# Finalizes a tag-triggered release: strips the trigger-tag prefix (old-/new-,
# see .evergreen.yml git_tag_aliases) off triggered_by_git_tag, pushes the
# bare "X.Y.Z" tag at the triggering commit, deletes the trigger tag, and
# flips the matching GH release from draft to published.
#
# Runs only after the whole release pipeline (build+test, or publish-only)
# has already succeeded, so "X.Y.Z" never points at a release that isn't
# actually ready. "X.Y.Z" is not itself a git_tag_aliases trigger, so pushing
# it here does not re-run anything.
#
# Usage: push_release_tag.sh [--dry-run] <trigger-tag-prefix>
# Expects GH_TOKEN (unused in --dry-run), triggered_by_git_tag, and revision.

set -Eeou pipefail

dry_run=false
if [[ "${1:-}" == "--dry-run" ]]; then
  dry_run=true
  shift
fi

prefix="${1:?usage: push_release_tag.sh [--dry-run] <trigger-tag-prefix>}"
trigger_tag="${triggered_by_git_tag:?triggered_by_git_tag is not set}"
revision="${revision:?revision is not set}"

version="${trigger_tag#"${prefix}"}"
if [[ "${version}" == "${trigger_tag}" ]]; then
  echo "triggered_by_git_tag '${trigger_tag}' does not start with expected prefix '${prefix}', aborting" >&2
  exit 1
fi

remote="https://x-access-token:${GH_TOKEN:-}@github.com/mongodb/mongodb-kubernetes.git"

if [[ "${dry_run}" == true ]]; then
  echo "dry-run: would run git tag -a ${version} -m \"Release of MCK ${version}\" ${revision}"
  echo "dry-run: would run git push <remote> refs/tags/${version}"
  echo "dry-run: would run git push <remote> :refs/tags/${trigger_tag}"
  echo "dry-run: would run gh release edit ${version} --draft=false --verify-tag --latest"
  exit 0
fi

git tag -a "${version}" -m "Release of MCK ${version}" "${revision}"
git push "${remote}" "refs/tags/${version}"
git push "${remote}" ":refs/tags/${trigger_tag}"

gh release edit "${version}" --draft=false --verify-tag --latest
