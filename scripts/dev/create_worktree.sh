#!/usr/bin/env bash

set -Eeou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

# Derive PROJECT_DIR from script location so it works even if the caller didn't export it.
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="${PROJECT_DIR:-$(cd "${script_dir}/../.." && pwd)}"
export PROJECT_DIR

usage() {
  echo "Initialize git worktree for the given branch."
  echo "It will create worktree and initialize it to be ready to use."
  echo "Usage: $0 [-f] <branch>"
  echo "  -f - force creation/initialization even if worktree already exists or branch is checked out"
  echo ""
  echo "Example: $0 -f user/feature-branch"
  echo "  This will create a worktree at ../user/feature-branch"
}

force=0
while getopts 'f' opt; do
    case ${opt} in
      (f)   force=1 ;;
      (*)   usage; exit 1;;
    esac
done
shift "$((OPTIND-1))"

branch="${1:-}"

if [[ -z ${branch} ]]; then
  echo "Error: missing branch parameter"
  usage
  exit 1
fi

# Convert slashes to underscores for directory name
branch_dir="${branch//\//_}"
# Canonicalise (no literal "..") so it matches `git worktree list` output.
worktree_path="$(realpath "${PROJECT_DIR}/..")/${branch_dir}"

# Exact match on column 1 so prefix-sharing siblings don't collide.
if ! git worktree list | awk '{print $1}' | grep -Fxq "${worktree_path}"; then
  echo "Creating git worktree for branch '${branch}' at ${worktree_path}..."

  # Check if branch exists (locally or remotely)
  branch_exists=0
  if git show-ref --verify --quiet "refs/heads/${branch}" || git show-ref --verify --quiet "refs/remotes/origin/${branch}"; then
    branch_exists=1
  fi

  if [[ ${branch_exists} == 1 ]]; then
    # Branch exists, use it
    if [[ ${force} == 1 ]]; then
      git worktree add -f "${worktree_path}" "${branch}" || true
    else
      git worktree add "${worktree_path}" "${branch}"
    fi
  else
    # Branch doesn't exist, create it from current branch
    echo "Branch '${branch}' does not exist. Creating it from current branch..."
    if [[ ${force} == 1 ]]; then
      git worktree add -f -b "${branch}" "${worktree_path}" || true
    else
      git worktree add -b "${branch}" "${worktree_path}"
    fi
  fi
fi

# Init on fresh (no private-context), partially-initialised (no venv), or -f.
if [[ ! -f "${worktree_path}/scripts/dev/contexts/private-context" \
      || ! -f "${worktree_path}/venv/bin/activate" \
      || ${force} == 1 ]]; then
  echo "Initializing worktree in ${worktree_path}..."
  init_flags=()
  [[ ${force} == 1 ]] && init_flags+=(-f)
  # Guarded expansion: empty array must not trip `set -u` on bash 3.2 (macOS).
  time "${PROJECT_DIR}/scripts/dev/init_worktree.sh" "${init_flags[@]+"${init_flags[@]}"}" "${worktree_path}" "${PROJECT_DIR}"
fi

(
  cd "${worktree_path}"
  make switch context="$(cat ".generated/.current_context")"
  source venv/bin/activate
  source .generated/context.export.env
)

echo "Worktree $(realpath "${worktree_path}") has been prepared"
