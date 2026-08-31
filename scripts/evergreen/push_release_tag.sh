#!/usr/bin/env bash

# Strips the trigger-tag prefix (old-/new-, see .evergreen.yml git_tag_aliases)
# off triggered_by_git_tag, pushes bare "X.Y.Z" at the triggering commit, and
# deletes the trigger tag. "X.Y.Z" is not itself a trigger, so this is safe.
#
# Usage: push_release_tag.sh <trigger-tag-prefix>
# Expects GH_TOKEN, triggered_by_git_tag, and revision.

set -Eeou pipefail

prefix="${1:?usage: push_release_tag.sh <trigger-tag-prefix>}"
trigger_tag="${triggered_by_git_tag:?triggered_by_git_tag is not set}"
revision="${revision:?revision is not set}"

version="${trigger_tag#"${prefix}"}"
if [[ "${version}" == "${trigger_tag}" ]]; then
  echo "triggered_by_git_tag '${trigger_tag}' does not start with expected prefix '${prefix}', aborting" >&2
  exit 1
fi

remote="https://x-access-token:${GH_TOKEN:-}@github.com/mongodb/mongodb-kubernetes.git"

# A "test-" prefix (only ever paired with the "new-" trigger prefix) marks a
# dry run: the trigger tag is deleted as usual, but the real, human-facing
# "X.Y.Z" tag must never be pushed, since it would disturb other PRs' next-
# version computation.
if [[ "${prefix}" == "new-" && "${version}" == test-* ]]; then
  echo "dry run detected (trigger tag '${trigger_tag}'), skipping push of real release tag"
else
  git tag -a "${version}" -m "Release of MCK ${version}" "${revision}"
  git push "${remote}" "refs/tags/${version}"
fi
git push "${remote}" ":refs/tags/${trigger_tag}"

