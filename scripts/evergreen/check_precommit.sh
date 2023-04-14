#!/usr/bin/env bash
set -Eeou pipefail

# shellcheck disable=SC2317
if [ -n "$(git diff --name-only --cached --diff-filter=AM)" ]; then
  echo "we have a dirty state, probably a patch, skipping this job!"
  exit 0
fi


export EVERGREEN_MODE=true
source .githooks/pre-commit

# shellcheck disable=SC2317
if [ -z "$(git diff --name-only --cached --diff-filter=AM)" ]; then
  echo "No changes detected, clean state"
  exit 0
else
  echo "We have files to be committed, please run the pre-commit locally"
  echo "The following files differ: "
  git diff --cached --diff-filter=AM
  exit 1
fi
