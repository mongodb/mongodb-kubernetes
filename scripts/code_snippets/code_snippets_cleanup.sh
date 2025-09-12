#!/bin/bash

set -eou pipefail

script_name=$(readlink -f "${BASH_SOURCE[0]}")
# shellcheck disable=SC2034
script_dir=$(dirname "${script_name}")

source scripts/code_snippets/sample_test_runner.sh

cleanup_directory() {
  local test_file="$1"
  local dir
  dir="$(dirname "${test_file}")"

  echo "    ${dir}"

  pushd "${dir}" >/dev/null
  run_cleanup "test.sh"
  run_cleanup "teardown.sh"
  rm -rf .generated 2>/dev/null || true
  rm -rf istio* 2>/dev/null || true
  rm -rf certs 2>/dev/null || true
  rm -rf secrets 2>/dev/null || true
  rm ./*.run.log 2>/dev/null || true
  popd >/dev/null
}

echo "Cleaning up from snippets runtime files from the following directories..."
for snippet_dir in $(bash "${script_dir}/find_snippets_directories.sh"); do
  cleanup_directory "${snippet_dir}/test.sh" &
done

wait
echo "Cleaning up done."

