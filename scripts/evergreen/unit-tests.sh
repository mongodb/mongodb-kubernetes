#!/usr/bin/env bash

set -Eeou pipefail

# shellcheck disable=SC2038
# shellcheck disable=SC2016
# TODO: MCK once we merge folder we need to update this
find . -name go.mod -not -path "./docker/mongodb-kubernetes-tests/*" -exec dirname "{}" \+ | xargs -L 1 /bin/bash -c '
cd "$0"
echo "testing $0"
rm -f result.suite

# Set verbose flag if VERBOSE is enabled
VERBOSE_FLAG=""
if [ "$VERBOSE" = "true" ]; then
  VERBOSE_FLAG="-v"
fi

RACE_FLAG=""
if [ "$USE_RACE" = "true" ]; then
  RACE_FLAG="-race"
fi

GO_TEST_CMD="go test $VERBOSE_FLAG $RACE_FLAG -coverprofile cover.out \$(go list ./... | grep -v \"mongodb-community-operator/test/e2e\")"
echo "running $GO_TEST_CMD"
eval "$GO_TEST_CMD" | tee -a result.suite
'

# Capture the exit status from the test runs
EXIT_STATUS=$?

# Generate summary of failed tests
echo ""
echo "========================================="
echo "TEST SUMMARY"
echo "========================================="

FAILED_TESTS=$(find . -name "result.suite" -not -path "./docker/mongodb-kubernetes-tests/*" -exec grep -l "FAIL" {} \; 2>/dev/null || true)

if [ -n "$FAILED_TESTS" ]; then
  echo "FAILED TEST MODULES:"
  for suite in $FAILED_TESTS; do
    MODULE_DIR=$(dirname "$suite")
    echo "  - $MODULE_DIR"
    echo "    Failed tests:"
    grep "^--- FAIL:" "$suite" | sed 's/^/      /' || true
  done
  echo ""
  echo "Total modules with failures: $(echo "$FAILED_TESTS" | wc -l | tr -d ' ')"
else
  echo " All tests passed!"
fi

echo "========================================="

# Exit with the original status to preserve CI behavior
exit $EXIT_STATUS
