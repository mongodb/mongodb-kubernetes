#!/usr/bin/env bash

set -eou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

source scripts/dev/set_env_context.sh

script_name=$(readlink -f "${BASH_SOURCE[0]}")

_SNIPPETS_OUTPUT_DIR="$(dirname "${script_name}")/outputs/$(basename "${script_name%.*}")"
export _SNIPPETS_OUTPUT_DIR
mkdir -p "${_SNIPPETS_OUTPUT_DIR}"

dump_logs() {
  if [[ "${SKIP_DUMP:-"false"}" != "true" ]]; then
    scripts/evergreen/e2e/dump_diagnostic_information_from_all_namespaces.sh "${K8S_CTX_0}"
    scripts/evergreen/e2e/dump_diagnostic_information_from_all_namespaces.sh "${K8S_CTX_1}"
  fi
}
trap dump_logs EXIT

# Phase 1: Infrastructure (scenario 12 — multi-cluster RS + managed LB)
test_dir="./docs/search/12-search-rs-multi-cluster"
source "${test_dir}/env_variables.sh"
echo "Sourcing env variables for ${CODE_SNIPPETS_FLAVOR} flavor"
# shellcheck disable=SC1090
test -f "${test_dir}/env_variables_${CODE_SNIPPETS_FLAVOR}.sh" && source "${test_dir}/env_variables_${CODE_SNIPPETS_FLAVOR}.sh"
${test_dir}/test.sh

# No restart sleep is needed here (unlike the single-cluster scenarios): on
# multi-cluster the search setParameters are part of the initial resource spec,
# so the mongod processes never restart after MongoDBSearch is created.

# Phase 2: RS queries (scenario 03) — runs against cluster 0
test_dir="./docs/search/03-search-query-usage"
echo "Sourcing env variables for ${CODE_SNIPPETS_FLAVOR} flavor"
# shellcheck disable=SC1090
test -f "${test_dir}/env_variables_${CODE_SNIPPETS_FLAVOR}.sh" && source "${test_dir}/env_variables_${CODE_SNIPPETS_FLAVOR}.sh"

# The query module is single-context: point it at the central cluster, where
# the tools pod and the CA ConfigMap live.
export K8S_CTX="${K8S_CTX_0}"

# Connection string uses the operator-managed per-pod Service names
export MDB_CONNECTION_STRING="${MDB_USER_CONNECTION_STRING}"

${test_dir}/test.sh
