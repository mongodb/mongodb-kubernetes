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
    for context in "${K8S_CTX_0:-}" "${K8S_CTX_1:-}" "${K8S_CTX_2:-}"; do
      if [[ -n "${context}" ]]; then
        scripts/evergreen/e2e/dump_diagnostic_information_from_all_namespaces.sh "${context}"
      fi
    done
  fi
}
trap dump_logs EXIT

# Multi-cluster External AppDB — reverse-migration
test_dir="./docs/external-appdb/multi-cluster/03-mc-reverse-migration"
source "${test_dir}/env_variables.sh"
echo "Sourcing env variables for ${CODE_SNIPPETS_FLAVOR} flavor"
# shellcheck disable=SC1090
test -f "${test_dir}/env_variables_${CODE_SNIPPETS_FLAVOR}.sh" && source "${test_dir}/env_variables_${CODE_SNIPPETS_FLAVOR}.sh"
${test_dir}/test.sh
