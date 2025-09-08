#!/usr/bin/env bash

set -eou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

source scripts/dev/set_env_context.sh

if [[ "${CODE_SNIPPETS_COMMIT_OUTPUT:-"false"}" == "true" ]]; then
  echo "Pushing output files"
  branch="meko-snippets-update-$(date "+%Y%m%d%H%M%S")"
  git checkout -b "${branch}"
  git reset
  git add scripts/code_snippets/tests/outputs/test_*
  git commit -m "Update code snippets outputs"
  git remote set-url origin "https://x-access-token:${GH_TOKEN}@github.com/mongodb/mongodb-kubernetes.git"
  git push origin "${branch}"
else
  echo "Not pushing output files"
fi
