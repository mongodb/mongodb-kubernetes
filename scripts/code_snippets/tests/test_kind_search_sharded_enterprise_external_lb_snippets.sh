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
    scripts/evergreen/e2e/dump_diagnostic_information_from_all_namespaces.sh "${K8S_CTX}"
  fi
}
trap dump_logs EXIT

test_dir="./docs/search/05-search-sharded-enterprise-external-lb"
source "${test_dir}/env_variables.sh"
echo "Sourcing env variables for ${CODE_SNIPPETS_FLAVOR} flavor"
# shellcheck disable=SC1090
test -f "${test_dir}/env_variables_${CODE_SNIPPETS_FLAVOR}.sh" && source "${test_dir}/env_variables_${CODE_SNIPPETS_FLAVOR}.sh"
${test_dir}/test.sh

echo "Sleeping for 120s to let sharded cluster nodes restart and configure with the search configuration."
sleep 120

# Determine TLS options based on whether TLS is enabled
TLS_OPTIONS=""
if [[ "${MDB_TLS_ENABLED:-false}" == "true" ]]; then
  TLS_OPTIONS="--tls --tlsAllowInvalidCertificates"
fi

# Verify the search configuration is applied by connecting directly to shard pods
echo "Verifying search configuration on shard pods..."
for i in $(seq 0 $((MDB_SHARD_COUNT - 1))); do
  shard_name="${MDB_RESOURCE_NAME}-${i}"
  pod_name="${shard_name}-0"
  echo "Checking shard ${i} (pod: ${pod_name})..."

  # Use mongosh on the shard pod to get the search parameters
  kubectl exec --context "${K8S_CTX}" -n "${MDB_NS}" "${pod_name}" -- /bin/bash -c '
    /var/lib/mongodb-mms-automation/mongosh-linux-x86_64-*/bin/mongosh \
      '"${TLS_OPTIONS}"' \
      --username mdb-admin \
      --password "'"${MDB_ADMIN_USER_PASSWORD}"'" \
      --authenticationDatabase admin \
      --quiet \
      --eval "
        const params = db.adminCommand({getParameter: 1, mongotHost: 1, searchIndexManagementHostAndPort: 1});
        print(\"mongotHost: \" + (params.mongotHost || \"not set\"));
        print(\"searchIndexManagementHostAndPort: \" + (params.searchIndexManagementHostAndPort || \"not set\"));
      "
  ' || echo "Search parameters not found for shard ${i}"
done

# Run search query tests for sharded clusters using the sharded test script
echo ""
echo "Running search query tests for sharded cluster..."
test_dir="./docs/search/03-search-query-usage"
${test_dir}/test_sharded.sh
