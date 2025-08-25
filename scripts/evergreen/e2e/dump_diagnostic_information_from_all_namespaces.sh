#!/usr/bin/env bash

## We need to make sure this script does not fail if one of
## the kubectl commands fails.
set +e

source scripts/funcs/printing
source scripts/evergreen/e2e/dump_diagnostic_information.sh

# If no context provided, use current context
if [ $# -eq 0 ]; then
  dump_all_non_default_namespaces "$(kubectl config current-context)"
else
  dump_all_non_default_namespaces "$@"
fi
