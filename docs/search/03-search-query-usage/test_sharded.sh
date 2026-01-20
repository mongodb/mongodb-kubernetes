#!/usr/bin/env bash

# Test script for search query usage on sharded clusters
# For sharded clusters, search indexes must be created and queries executed
# directly on each shard because mongos doesn't have the mongotHost parameter configured

set -eou pipefail

script_name=$(readlink -f "${BASH_SOURCE[0]}")
script_dir=$(dirname "${script_name}")

source "${script_dir}/../../../scripts/code_snippets/sample_test_runner.sh"

cd "${script_dir}"

prepare_snippets

# Validate required environment variables for sharded clusters
: "${MDB_SHARD_COUNT:?MDB_SHARD_COUNT must be set for sharded cluster tests}"
: "${MDB_RESOURCE_NAME:?MDB_RESOURCE_NAME must be set}"
: "${MDB_NS:?MDB_NS must be set}"
: "${K8S_CTX:?K8S_CTX must be set}"
: "${MDB_ADMIN_USER_PASSWORD:?MDB_ADMIN_USER_PASSWORD must be set}"

echo "Running search query tests for sharded cluster with ${MDB_SHARD_COUNT} shards..."
echo ""

# Note: For sharded clusters, we don't need the mongodb-tools-pod since we execute
# commands directly on the shard pods using the built-in mongosh

run 03_0421_import_movies_to_shards.sh
run 03_0431_create_search_index_on_shards.sh
run_for_output 03_0441_wait_for_search_index_ready_on_shards.sh
run_for_output 03_0446_list_search_indexes_on_shards.sh
run_for_output 03_0451_execute_search_query_on_shards.sh

cd -
