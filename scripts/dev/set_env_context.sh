#!/usr/bin/env bash

set -Eeou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

# shellcheck disable=1091
source scripts/funcs/errors

script_name=$(readlink -f "${BASH_SOURCE[0]}")
script_dir=$(dirname "${script_name}")
context_file="$(realpath "${script_dir}/../../.generated/context.export.env")"

if [[ ! -f ${context_file} ]]; then
    fatal "File ${context_file} not found! Make sure to follow this guide to get started: https://wiki.corp.mongodb.com/display/MMS/Setting+up+local+development+and+E2E+testing#SettinguplocaldevelopmentandE2Etesting-GettingStartedGuide(VariantSwitching)"
fi

# Side guard: refuse to source a context.export.env that was generated on the
# OTHER side (host vs. devcontainer). Without this, sourcing a container-side
# file from the host clobbers PATH with /workspace/bin:/go/bin:... and breaks
# every host command. switch_context.sh writes a `## Side: host|container`
# stamp into the canonical .env (sibling of context.export.env); read it
# (without sourcing) and compare against /.dockerenv.
side_stamp_file="${context_file%.export.env}.env"
if [[ -f "${side_stamp_file}" ]]; then
    file_side="$(grep -m1 '^## Side: ' "${side_stamp_file}" | awk '{print $3}' || true)"
    current_side="$([[ -f /.dockerenv ]] && echo container || echo host)"
    if [[ -n "${file_side}" && "${file_side}" != "${current_side}" ]]; then
        fatal "${side_stamp_file} was generated on side='${file_side}' but you're sourcing it on side='${current_side}'. Re-run 'make switch' (or scripts/dev/switch_context.sh <ctx>) on the ${current_side} side to regenerate it."
    fi
fi

# shellcheck disable=SC1090
source "${context_file}"

export PATH="${PROJECT_DIR}/bin:${PATH}"
