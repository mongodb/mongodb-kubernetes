#!/usr/bin/env bash

set -eou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

source scripts/dev/set_env_context.sh

# task_name is available from evergreen expansions and its set to the snippets test name (see tasks in .evergreen-snippets.yml)
test_name=${task_name:?}
test_name=${test_name%.sh}
patch_id=${version_id:?}

# to run it locally:
# $ test_name=test_kind_search_enterprise_snippets.sh archive_snippets_output.sh

output_dir="scripts/code_snippets/tests/outputs/${test_name}"
if [[ ! -d "${output_dir}" ]]; then
  echo "Output dir is missing: ${output_dir}"
  exit 1
fi

file_name="snippets_outputs_${patch_id}_${test_name}.tgz"
tar -cvzf "${file_name}" "${output_dir}"

echo "Collected snippets outputs from ${test_name} into ${file_name}"
