#!/usr/bin/env bash

set -eou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

source scripts/dev/set_env_context.sh

# It finds all directories containing both test.sh and code_snippets subdirectory
find . -path "*/code_snippets" -type d | while read -r code_snippets_dir; do
  parent_dir="$(dirname "${code_snippets_dir}")"
  if [[ -f "${parent_dir}/test.sh" ]]; then
    echo "${parent_dir}"
  fi
done
