#!/usr/bin/env bash

set -eou pipefail
source scripts/dev/set_env_context.sh

dump_logs() {
  source scripts/evergreen/e2e/dump_diagnostic_information.sh
  if [[ "${SKIP_DUMP:-"false"}" != "true" ]]; then
    dump_all_non_default_namespaces "$@"
  fi
}
trap dump_logs EXIT

test_dir="./docs/community-search/quick-start"

source "${test_dir}/env_variables.sh"
echo "Sourcing env variables for ${CODE_SNIPPETS_FLAVOR} flavor"
# shellcheck disable=SC1090
test -f "${test_dir}/env_variables_${CODE_SNIPPETS_FLAVOR}.sh" && source "${test_dir}/env_variables_${CODE_SNIPPETS_FLAVOR}.sh"

${test_dir}/test.sh
scripts/code_snippets/kind_community_search_snippets_render_template.sh
