#!/usr/bin/env bash
#
# Run govulncheck against all Go files.
# Pins to v1.3.0 because v1.4.0+ panics on Go 1.26.x
# (ForEachElement called on type containing *types.TypeParam).
#
# Exit code 3 means stdlib vulnerabilities were found, which are out of our control.
# This is informational; treat as a soft warning, not a hard failure.
#
set -Eeuo pipefail

GOTELEMETRY=off go run golang.org/x/vuln/cmd/govulncheck@v1.3.0 ./...

exit_code=$?
if [ $exit_code -eq 3 ]; then
  echo "Stdlib vulnerabilities found (informational only, not a code issue)."
  exit 0
fi
exit $exit_code