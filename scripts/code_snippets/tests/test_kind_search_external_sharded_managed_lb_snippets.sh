#!/usr/bin/env bash
# Test script for MongoDB Search with External Sharded MongoDB + Managed Envoy LB
#
# This test runs the code snippets for deploying MongoDB Search against an
# external sharded MongoDB cluster using the operator's managed Envoy L7 LB.

set -eou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

source scripts/dev/set_env_context.sh

script_name=$(readlink -f "${BASH_SOURCE[0]}")

_SNIPPETS_OUTPUT_DIR="$(dirname "${script_name}")/outputs/$(basename "${script_name%.*}")"
export _SNIPPETS_OUTPUT_DIR
mkdir -p "${_SNIPPETS_OUTPUT_DIR}"

dump_logs() {
  if [[ "${SKIP_DUMP:-"false"}" != "true" ]]; then
    scripts/evergreen/e2e/dump_diagnostic_information_from_all_namespaces.sh "${K8S_CTX}"
  fi
}
trap dump_logs EXIT

test_dir="./docs/search/07-search-external-sharded-mongod-managed-lb"
source "${test_dir}/env_variables.sh"
echo "Sourcing env variables for ${CODE_SNIPPETS_FLAVOR} flavor"
# shellcheck disable=SC1090
test -f "${test_dir}/env_variables_${CODE_SNIPPETS_FLAVOR}.sh" && source "${test_dir}/env_variables_${CODE_SNIPPETS_FLAVOR}.sh"
${test_dir}/test.sh

echo "Test completed successfully. External Sharded Search with Managed Envoy LB verified."

