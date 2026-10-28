#!/usr/bin/env bash

set -Eeou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

# shellcheck disable=1091
source scripts/funcs/errors

script_name=$(readlink -f "${BASH_SOURCE[0]}")
script_dir=$(dirname "${script_name}")

# Sourced: return, not exit, so that a caller can handle the failure with `||`. The `set -e` above stays on in the
# caller and stops it otherwise. A shell that is not a script sources scripts/dev/devenv, which turns no option on.
# shellcheck disable=SC1091
source "${script_dir}/devenv" || return 1
