#!/usr/bin/env bash

set -Eeou pipefail

## We need to make sure this script does not fail if one of
## the kubectl commands fails.
set +e

source scripts/funcs/printing
source scripts/evergreen/e2e/dump_diagnostic_information.sh

dump_all_non_default_namespaces "$@"
