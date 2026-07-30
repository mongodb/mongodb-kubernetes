#!/usr/bin/env bash

set -eou pipefail

script_name=$(readlink -f "${BASH_SOURCE[0]}")
script_dir=$(dirname "${script_name}")

source scripts/code_snippets/sample_test_runner.sh

pushd "${script_dir}"

source env_variables.sh

prepare_snippets

run ra-01_9010_delete_gke_clusters.sh
run ra-01_9020_delete_disks.sh
run ra-01_9030_delete_firewall_rules.sh

popd
