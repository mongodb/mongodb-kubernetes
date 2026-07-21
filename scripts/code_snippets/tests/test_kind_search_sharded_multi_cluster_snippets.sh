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
    for context in "${K8S_CTX_0:-}" "${K8S_CTX_1:-}"; do
      if [[ -n "${context}" ]]; then
        scripts/evergreen/e2e/dump_diagnostic_information_from_all_namespaces.sh "${context}"
      fi
    done
  fi
}
trap dump_logs EXIT

# Phase 1: Infrastructure (scenario 13 — multi-cluster sharded + managed LB)
test_dir="./docs/search/13-search-sharded-multi-cluster"
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

# Phase 2: Sharded queries (scenario 08) — runs against cluster 0
test_dir="./docs/search/08-search-sharded-query-usage"
echo "Sourcing env variables for ${CODE_SNIPPETS_FLAVOR} flavor"
# shellcheck disable=SC1090
test -f "${test_dir}/env_variables_${CODE_SNIPPETS_FLAVOR}.sh" && source "${test_dir}/env_variables_${CODE_SNIPPETS_FLAVOR}.sh"

# The query module is single-context: point it at the central cluster, where
# the tools pod and the CA ConfigMap live. MDB_ADMIN_CONNECTION_STRING and
# MDB_USER_CONNECTION_STRING are exported by scenario 13's env_variables.sh.
export K8S_CTX="${K8S_CTX_0}"

${test_dir}/test.sh

assert_search_results() {
  kubectl exec -i mongodb-tools -n "${MDB_NS}" --context "${K8S_CTX}" -- \
    mongosh --quiet "${MDB_USER_CONNECTION_STRING}" <<'MONGOSH'
const sampleDb = db.getSiblingDB("sample_mflix");
const textResults = sampleDb.movies.aggregate([
  { $search: { text: { query: "baseball", path: "plot" } } },
  { $limit: 1 }
]);
if (!textResults.hasNext()) {
  throw new Error("internal text search assertion returned no documents");
}

const queryVector = sampleDb.embedded_movies.findOne(
  { plot_embedding_voyage_3_large: { $exists: true } },
  { plot_embedding_voyage_3_large: 1 }
).plot_embedding_voyage_3_large;
const vectorResults = sampleDb.embedded_movies.aggregate([
  {
    $vectorSearch: {
      index: "vector_index",
      path: "plot_embedding_voyage_3_large",
      queryVector,
      numCandidates: 50,
      limit: 1
    }
  }
]);
if (!vectorResults.hasNext()) {
  throw new Error("internal vector search assertion returned no documents");
}
MONGOSH
}

# Phase 3 proves that a mongos in each cluster can execute Search queries
# through cluster 0's stable proxy targets. The cluster-0 tools pod reaches
# both mongos processes over the mesh. K8S_CTX stays K8S_CTX_0 because the
# tools pod and CA live there.
# Invoke via 'bash -e' rather than the run/run_for_output framework: its skip-log
# is keyed on snippet name, so a second run() of the same snippet would no-op.
for ci in 0 1; do
  mongos_var="MDB_EXTERNAL_MONGOS_HOST_${ci}"
  mongos="${!mongos_var}"
  export MDB_USER_CONNECTION_STRING="mongodb://mdb-user:${MDB_USER_PASSWORD}@${mongos}/?directConnection=true&tls=true&tlsCAFile=/tls/ca.crt&authSource=admin&authMechanism=SCRAM-SHA-256"
  echo "Phase 3: verifying search from cluster ${ci} mongos (${mongos})"
  bash -e ./docs/search/08-search-sharded-query-usage/code_snippets/08_0450_execute_search_query.sh
  bash -e ./docs/search/08-search-sharded-query-usage/code_snippets/08_0455_execute_vector_search_query.sh
  assert_search_results
done
