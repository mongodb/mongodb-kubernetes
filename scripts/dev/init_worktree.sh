#!/usr/bin/env bash
#
# Initializes a linked worktree: copies per-worktree files, runs make switch,
# and recreates the python venv.
#
# Usage: init_worktree.sh [-f] <worktree_root> <main_repo_root>
#   -f  overwrite files that already exist (default: skip)
#

set -Eeou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

force=0
while getopts 'f' opt; do
    case ${opt} in
      (f) force=1 ;;
      (*) echo "Usage: $0 [-f] <worktree_root> <main_repo_root>"; exit 1 ;;
    esac
done
shift "$((OPTIND-1))"

worktree_root="${1}"
main_repo_root="${2}"

maybe_copy() {
    local rel_path="${1}"
    local source="${2}"
    local dest="${worktree_root}/${rel_path}"

    if [[ -e "${dest}" && ${force} == 0 ]]; then
        echo "init_worktree: '${rel_path}' already exists, skipping."
        return
    fi

    if [[ ! -e "${source}" ]]; then
        echo "init_worktree: source '${source}' not found, skipping."
        return
    fi

    mkdir -p "$(dirname "${dest}")"
    cp -r "${source}" "${dest}"
    echo "init_worktree: copied '${rel_path}'"
}

maybe_copy "scripts/dev/contexts/private-context" "${main_repo_root}/scripts/dev/contexts/private-context"
maybe_copy ".generated"                            "${main_repo_root}/.generated"
maybe_copy "bin"                                   "${main_repo_root}/bin"

current_context_file="${worktree_root}/.generated/.current_context"
if [[ -f "${current_context_file}" ]]; then
    context="$(cat "${current_context_file}")"
    echo "init_worktree: running 'make switch context=${context}'"
    (cd "${worktree_root}" && make switch context="${context}")
else
    echo "init_worktree: no .generated/.current_context found, skipping 'make switch'"
fi

if [[ ! -d "${worktree_root}/venv" || ${force} == 1 ]]; then
    echo "init_worktree: recreating python venv"
    (cd "${worktree_root}" && scripts/dev/recreate_python_venv.sh)
else
    echo "init_worktree: 'venv' already exists, skipping."
fi
