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

# All local worktrees share Python venvs so that switching between worktrees doesn't
# require recreating them each time. They live under ~/.operator-dev by default and are
# keyed by PYTHON_VERSION (e.g. ~/.operator-dev/venv-3.13.7) so worktrees pinning
# different Python versions don't clobber each other's venv.
# Override the location by exporting PROJECT_VENV_PATH before sourcing this script.
# In Evergreen each task has its own isolated workdir, so the venv stays inside PROJECT_DIR.
if [[ -z "${PROJECT_VENV_PATH:-}" ]]; then
    if [[ "${RUNNING_IN_EVG:-}" == "true" ]]; then
        export PROJECT_VENV_PATH="${PROJECT_DIR}/venv"
    elif [[ -n "${PYTHON_VERSION:-}" ]]; then
        export PROJECT_VENV_PATH="${HOME}/.operator-dev/venv-${PYTHON_VERSION}"
    else
        export PROJECT_VENV_PATH="${HOME}/.operator-dev/venv"
    fi
fi

export PATH="${PROJECT_DIR}/bin:${PATH}"
