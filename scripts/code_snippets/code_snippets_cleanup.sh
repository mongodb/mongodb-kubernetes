#!/usr/bin/env bash

set -eou pipefail

find public/architectures -name "test.sh" -exec sh -c '
  source scripts/code_snippets/sample_test_runner.sh

  pushd "$(dirname $1)"
  run_cleanup "test.sh"
  run_cleanup "teardown.sh"
  rm -rf istio*
  rm -rf certs

  popd
  ' sh {} \;
