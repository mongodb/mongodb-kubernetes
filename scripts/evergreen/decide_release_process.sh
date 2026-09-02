#!/usr/bin/env bash

# Reads the git tag annotation to decide which release pipeline to run
# and creates an Evergreen patch via the matching alias, waiting for it.
# "[new]" MUST be the first token in the annotation; "[dry-run]" is optional.
#
# Usage: decide_release_process.sh <tag_name>

set -Eeou pipefail

tag="${1:?usage: decide_release_process.sh <tag_name>}"
annotation=$(git tag -l --format='%(contents:subject)' "${tag}")

if [[ "${annotation}" == "[new]"* ]]; then
  alias=release-publish
else
  alias=deprecated-release
fi

params=()
if [[ "${annotation}" == *"[dry-run]"* ]]; then
  params+=(--param "IS_DRYRUN=true")
  echo "Dry run enabled: setting IS_DRYRUN=true"
fi

echo "Creating Evergreen patch with alias '${alias}' for release ${tag}"
evergreen patch \
  -p mongodb-kubernetes \
  --alias "${alias}" \
  -d "Release ${tag}" \
  --param "triggered_by_git_tag=${tag}" \
  "${params[@]}" \
  -f -y -w

