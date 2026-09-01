#!/usr/bin/env bash

# Triggers the public docs-snippet refresh for a released version.
# Master only (backports don't change snippet content); checked via ${revision} vs origin/master
# rather than branch_name, since a tag/workflow_dispatch build's branch_name isn't reliable here.
# Manual fallback for backports:
#   evergreen patch -p mongodb-kubernetes -v public_kind_code_snippets -v public_gke_code_snippets -t all -f -y -u --browse

set -Eeou pipefail

: "${revision:?revision must be set (Evergreen expansion)}"
: "${triggered_by_git_tag:?triggered_by_git_tag must be set}"

git fetch origin master --quiet

if ! git merge-base --is-ancestor "${revision}" origin/master; then
  echo "trigger_public_docs_snippets: ${revision} is not on master (backport release ${triggered_by_git_tag}) - skipping"
  exit 0
fi

echo "trigger_public_docs_snippets: triggering public snippet refresh for released version ${triggered_by_git_tag}"
evergreen patch \
  -p mongodb-kubernetes \
  -v public_kind_code_snippets \
  -v public_gke_code_snippets \
  -t all \
  -d "public docs snippets refresh for ${triggered_by_git_tag}" \
  -f -y -u
