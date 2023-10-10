#!/usr/bin/env bash

set -Eeou pipefail

# shellcheck disable=1091
source scripts/funcs/errors

script_name=$(readlink -f "${BASH_SOURCE[0]}")
script_dir=$(dirname "${script_name}")
context_file="$script_dir/../../.generated/context.export.env"

if [[ ! -f ${context_file} ]]; then
    fatal "File ${context_file} not found! You must init development environment using 'make init' first."
fi

# shellcheck disable=SC1090
source "$context_file"
