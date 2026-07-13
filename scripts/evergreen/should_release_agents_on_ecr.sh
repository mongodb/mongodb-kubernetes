#!/usr/bin/env bash

# This file is a condition script used for conditionally executing evergreen task for releasing agents to ecr.

set -Eeou pipefail

no_local_changes() {
  git diff --quiet -- release.json
}

no_changes_vs_branch() {
  git diff --quiet "origin/${branch_name:-master}" -- release.json
}

git fetch origin

if no_local_changes && no_changes_vs_branch; then
  echo "skipping since release.json has not changed"
  exit 1
else
  echo "release.json has changed, triggering ecr agent release"
  echo "git diff"
  git diff -- release.json
  echo "git diff ${branch_name:-master}"
  git diff "origin/${branch_name:-master}" -- release.json
  exit 0
fi
