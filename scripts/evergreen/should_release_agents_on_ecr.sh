#!/usr/bin/env bash

# This file is a condition script used for conditionally executing evergreen task for releasing agents to ecr.

set -Eeou pipefail -o posix

no_local_changes() {
  git diff --quiet -- release.json
}

no_changes_vs_master() {
  git diff --quiet origin/master -- release.json
}

git fetch origin

if no_local_changes && no_changes_vs_master; then
  echo "skipping since release.json has not changed"
  exit 1
else
  echo "release.json has changed, triggering ecr agent release"
  echo "git diff"
  git diff -- release.json
  echo "git diff master"
  git diff origin/master -- release.json
  exit 0
fi
