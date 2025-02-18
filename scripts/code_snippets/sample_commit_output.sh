#!/usr/bin/env bash

set -eou pipefail
source scripts/dev/set_env_context.sh

if [ "${COMMIT_OUTPUT:-false}" = true ]; then
  echo "Pushing output files"
  branch="meko-snippets-update-$(date "+%Y%m%d%H%M%S")"
  git checkout -b "${branch}"
  git reset
  git add public/architectures/**/*.out
  git commit -m "Update code snippets outputs"
  git push --set-upstream origin "${branch}"
else
  echo "Not pushing output files"
fi
