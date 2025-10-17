#!/usr/bin/env bash

set -Eeou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

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

branch=$1

if [[ -z ${branch} ]]; then
  echo "Error: missing branch parameter"
  usage
  exit 1
fi

# Convert slashes to underscores for directory name
branch_dir="${branch//\//_}"
worktree_path="${PROJECT_DIR}/../${branch_dir}"

if ! git worktree list | grep "${worktree_path}" >/dev/null; then
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

if [[ ! -f "${worktree_path}/scripts/dev/contexts/private-context" || ${force} == 1 ]]; then
  echo "Initializing worktree in ${worktree_path}..."
  time (
    mkdir -p "${worktree_path}/.generated"
    cp "${PROJECT_DIR}/.generated/.current_context" "${worktree_path}/.generated/"
    cp "${PROJECT_DIR}/scripts/dev/contexts/private-context" "${worktree_path}/scripts/dev/contexts/private-context"
    cp -r "${PROJECT_DIR}/bin" "${worktree_path}/bin"
    (
      cd "${worktree_path}"
      make switch context="$(cat ".generated/.current_context")"
      scripts/dev/recreate_python_venv.sh
    )
  )
fi

(
  cd "${worktree_path}"
  make switch context="$(cat ".generated/.current_context")"
  source venv/bin/activate
  source .generated/context.export.env
)

# temporarily copying the files as those are not committed to master yet and won't be available in new git workspace
cp -r "${PROJECT_DIR}/.run" "${worktree_path}/"
cp -r "${PROJECT_DIR}/.idea" "${worktree_path}/"
rm "${worktree_path}/.idea/workspace.xml"
cp -r ${PROJECT_DIR}/scripts/aliases ${worktree_path}/scripts/aliases

echo "Worktree $(realpath "${worktree_path}") has been prepared"
