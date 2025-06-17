#!/usr/bin/env bash

set -Eeou pipefail -o posix

# shellcheck disable=1091
source scripts/funcs/errors

script_name=$(readlink -f "${BASH_SOURCE[0]}")
script_dir=$(dirname "${script_name}")
context_file="${script_dir}/../../.generated/context.export.env"

if [[ ! -f ${context_file} ]]; then
    fatal "File ${context_file} not found! Make sure to follow this guide to get started: https://wiki.corp.mongodb.com/display/MMS/Setting+up+local+development+and+E2E+testing#SettinguplocaldevelopmentandE2Etesting-GettingStartedGuide(VariantSwitching)"
fi

# shellcheck disable=SC1090
source "${context_file}"

export PATH="${PROJECT_DIR}/bin:${PATH}"
