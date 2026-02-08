#!/usr/bin/env bash

set -Eeou pipefail

source scripts/funcs/errors

script_name=$(readlink -f "${BASH_SOURCE[0]}")
script_dir=$(dirname "${script_name}")

generated_context_dir="${script_dir}/../../.generated"
mkdir -p "${generated_context_dir}" >/dev/null
generated_context_dir="$(realpath "${generated_context_dir}")"

current_context_file="${generated_context_dir}/.current_context"
if [[ -f "${current_context_file}" ]]; then
  current_context=$(cat "${current_context_file}")
else
  echo "Current context is not set in ${current_context_file}. Using root-context."
  current_context="root-context"
fi

scripts/dev/switch_context.sh "${current_context}"
