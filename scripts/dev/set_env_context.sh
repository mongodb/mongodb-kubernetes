#!/usr/bin/env bash

set -Eeou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

# shellcheck disable=1091
source scripts/funcs/errors

# The caller runs from the repository root. Resolve the main clone from the
# shared git dir so a linked worktree can initialize itself from the main
# clone's context, bin, and venv.
main_repo="$(cd "$(git rev-parse --git-common-dir)/.." && pwd)"
context_file="${PWD}/.generated/context.export.env"

if [[ ! -f ${context_file} && ${main_repo} != "${PWD}" ]]; then
    PROJECT_VENV_PATH="${main_repo}/venv" PATH="${PWD}/bin:${PATH}" scripts/dev/init_worktree.sh "${PWD}" "${main_repo}"
fi

if [[ ! -f ${context_file} ]]; then
    fatal "File ${context_file} not found! Make sure to follow this guide to get started: https://wiki.corp.mongodb.com/display/MMS/Setting+up+local+development+and+E2E+testing#SettinguplocaldevelopmentandE2Etesting-GettingStartedGuide(VariantSwitching)"
fi

# shellcheck disable=SC1090
source "${context_file}"

# Activate the python venv here so consumer scripts don't each activate it.
# Per-worktree by default; fall back to the main clone venv when the worktree
# has none and PROJECT_VENV_PATH is unset.
venv_path="${PROJECT_VENV_PATH:-${PROJECT_DIR}/venv}"
if [[ ! -f "${venv_path}/bin/activate" && -z ${PROJECT_VENV_PATH:-} && -f "${main_repo}/venv/bin/activate" ]]; then
    venv_path="${main_repo}/venv"
fi
if [[ -f "${venv_path}/bin/activate" ]]; then
    # shellcheck disable=SC1091
    source "${venv_path}/bin/activate"
fi

export PATH="${PROJECT_DIR}/bin:${PATH}"
