#!/usr/bin/env bash

set -Eeou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

# shellcheck disable=1091
source scripts/funcs/errors

script_name=$(readlink -f "${BASH_SOURCE[0]}")
script_dir=$(dirname "${script_name}")

tmpfile="$(mktemp)"
if ! scripts/dev/regenerate_context.sh >"${tmpfile}" 2>&1; then
  cat "${tmpfile}"
  rm "${tmpfile}"
  exit 1
fi
context_file="$(realpath "${script_dir}/../../.generated/context.export.env")"
current_context="$(cat "$(dirname "${context_file}")/.current_context")"
echo "Using context ${current_context} (${context_file})" >&2
# shellcheck disable=SC1090
source "${context_file}"

export PATH="${PROJECT_DIR}/bin:${PATH}"
