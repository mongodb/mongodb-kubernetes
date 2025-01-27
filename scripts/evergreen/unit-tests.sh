#!/usr/bin/env bash

set -Eeou pipefail

rm -f ops-manager-kubernetes.suite

# shellcheck disable=SC2038
# shellcheck disable=SC2016
find . -name go.mod -exec dirname "{}" \+ | xargs -L 1 /bin/bash -o pipefail -c '
cd "$0"
echo "testing $0"
if [ "$USE_RACE" = "true" ]; then
  echo "running test with race enabled"
  GO_TEST_CMD="go test -race -coverprofile cover.out ./..."
else
  echo "running test without race enabled"
  GO_TEST_CMD="go test -coverprofile cover.out ./..."
fi
echo "running $GO_TEST_CMD"
eval "$GO_TEST_CMD" | tee -a ops-manager-kubernetes.suite
if [ $? -ne 0 ]; then
  echo "Tests failed in directory $0"
  grep "FAIL:" ops-manager-kubernetes.suite
  echo ""
  exit 1
fi
'
