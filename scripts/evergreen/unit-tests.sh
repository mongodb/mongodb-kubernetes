#!/usr/bin/env bash

set -Eeou pipefail

rm -f result.suite

# shellcheck disable=SC2038
# shellcheck disable=SC2016
find . -name go.mod -exec dirname "{}" \+ | xargs -L 1 /bin/bash -o pipefail -c '
cd "$0"
echo "testing $0"
if [ "$USE_RACE" = "true" ]; then
  echo "running test with race enabled"
  GO_TEST_CMD="go test -v -race -coverprofile cover.out ./..."
else
  echo "running test without race enabled"
  GO_TEST_CMD="go test -v -coverprofile cover.out ./..."
fi
echo "running $GO_TEST_CMD"
eval "$GO_TEST_CMD" | tee -a result.suite
if [ $? -ne 0 ]; then
  echo "Tests failed in directory $0"
  grep "FAIL:" result.suite
  echo ""
  exit 1
fi
'
