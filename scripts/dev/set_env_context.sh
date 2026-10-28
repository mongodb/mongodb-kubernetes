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

# shellcheck disable=SC1090
source "${context_file}"

# Activate the python venv here so consumer scripts don't each activate it.
# Per-worktree by default; opt into a shared venv by exporting PROJECT_VENV_PATH.
venv_path="${PROJECT_VENV_PATH:-${PROJECT_DIR}/venv}"
if [[ -f "${venv_path}/bin/activate" ]]; then
    # shellcheck disable=SC1091
    source "${venv_path}/bin/activate"
fi

export PATH="${PROJECT_DIR}/bin:${PATH}"

# Scoped mode (MCK_DEVC_LOCAL_KIND=1) keeps the clusters it creates out of the user's
# ~/.kube/config: every script that sources this file, and `kind export`, use the
# kubeconfig of the checkout.
if [[ "${MCK_DEVC_LOCAL_KIND:-}" == "1" ]]; then
    export KUBECONFIG="${PROJECT_DIR}/.generated/current.kubeconfig"
fi
