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
# shellcheck disable=SC1091
source "${test_dir}/env_variables_internal.sh"
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

${test_dir}/test.sh

# Phase 3: per-cluster query verification — prove each member cluster serves
# $search/$vectorSearch. Reuse scenario 03's execute snippets, re-pointed at each
# cluster's own node via directConnection (the cluster-0 tools pod reaches them
# over the mesh). Reuse the per-cluster host vars that env_variables_e2e_private.sh
# exports (same values Phase 2 connects to). K8S_CTX stays K8S_CTX_0 (tools pod +
# CA live there).
# Invoke via 'bash -e' rather than the run/run_for_output framework: its skip-log
# is keyed on snippet name, so a second run() of the same snippet would no-op.
for ci in 0 1; do
  host_var="MDB_EXTERNAL_HOST_${ci}_0"
  member="${!host_var}"
  export MDB_CONNECTION_STRING="mongodb://mdb-user:${MDB_USER_PASSWORD}@${member}/?directConnection=true&readPreference=secondaryPreferred&tls=true&tlsCAFile=/tls/ca.crt&authSource=admin&authMechanism=SCRAM-SHA-256"
  echo "Phase 3: verifying search from cluster ${ci} (${member})"
  bash -e ./docs/search/03-search-query-usage/code_snippets/03_0450_execute_search_query.sh
  bash -e ./docs/search/03-search-query-usage/code_snippets/03_0455_execute_vector_search_query.sh
done
