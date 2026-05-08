#!/usr/bin/env bash
#
# Tear down a dev worktree set up by wt_setup.sh:
#   - delete the per-worktree cloud-qa OM projects (so they don't orphan)
#   - stop & remove the devcontainer compose stack
#   - terminate the EVG host with that displayName
#   - remove the git worktree directory
#   - optionally delete the branch (--delete-branch)
#
# Usage:
#   wt_teardown.sh [options] <branch>
#
# Options:
#   --delete-branch     Also delete the local branch after removing the worktree.
#   --keep-evg-host     Don't terminate the EVG host (default: terminate).
#   --keep-stack        Don't run `docker compose down` (default: down).
#   --keep-worktree     Don't `git worktree remove` (default: remove).
#   --keep-om-projects  Don't run delete_om_projects.sh against the worktree's
#                       ls-${MCK_DEVC_NET_PREFIX}-* scope (default: delete).
#   --evg-host-name N   Override EVG host display name (default: worktree dir).
#   -h, --help
#
# Each step is best-effort; failures don't abort subsequent steps. If a step
# is already done (host already terminated, worktree already gone), the
# script reports it and moves on.

set -Eeou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

usage() { sed -n '3,23p' "$0"; }

delete_branch=0
keep_evg_host=0
keep_stack=0
keep_worktree=0
keep_om_projects=0
evg_host_name=""
positional=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --delete-branch)    delete_branch=1; shift ;;
    --keep-evg-host)    keep_evg_host=1; shift ;;
    --keep-stack)       keep_stack=1; shift ;;
    --keep-worktree)    keep_worktree=1; shift ;;
    --keep-om-projects) keep_om_projects=1; shift ;;
    --evg-host-name)    evg_host_name="$2"; shift 2 ;;
    -h|--help)          usage; exit 0 ;;
    -*) echo "Unknown option: $1" >&2; usage; exit 1 ;;
    *) positional+=("$1"); shift ;;
  esac
done

if [[ ${#positional[@]} -ne 1 ]]; then
  echo "ERROR: exactly one branch name is required." >&2
  usage; exit 1
fi
branch="${positional[0]}"
branch_dir="${branch//\//_}"

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
src_repo_root="$(cd "${script_dir}/../.." && pwd)"
worktree_path="${src_repo_root}/../${branch_dir}"
worktree_path="$(cd "$(dirname "${worktree_path}")" && pwd)/$(basename "${worktree_path}")"

[[ -z "${evg_host_name}" ]] && evg_host_name="${branch_dir}"

echo "==> teardown: branch=${branch}, worktree=${worktree_path}, evg_host=${evg_host_name}"

# 1. Delete the per-worktree cloud-qa OM projects.
#
# delete_om_projects.sh's NAMESPACE-based filter is the only thing scoping
# the cleanup; with per-worktree NAMESPACE=ls-${MCK_DEVC_NET_PREFIX} (set by
# root-context when this worktree's .devcontainer/.env carries the prefix)
# the script only deletes this worktree's projects. Run this BEFORE compose
# teardown so failures here don't leave the stack hanging, and BEFORE
# worktree removal so the script can read the worktree's .devcontainer/.env.
if [[ ${keep_om_projects} -eq 0 && -d "${worktree_path}" ]]; then
  if [[ -f "${worktree_path}/.devcontainer/.env" ]] \
       && grep -q '^MCK_DEVC_NET_PREFIX=' "${worktree_path}/.devcontainer/.env"; then
    echo "==> Deleting cloud-qa OM projects scoped to this worktree"
    ( cd "${worktree_path}" && bash scripts/dev/delete_om_projects.sh ) 2>&1 \
      | sed 's/^/    /' \
      || echo "    (OM project cleanup hit an error; you may need to delete leftovers manually)"
  else
    echo "==> No MCK_DEVC_NET_PREFIX in ${worktree_path}/.devcontainer/.env; skipping OM cleanup"
  fi
fi

# 2. docker compose down (project name = <basename>_devcontainer).
if [[ ${keep_stack} -eq 0 ]]; then
  project_name="${branch_dir}_devcontainer"
  if docker compose -p "${project_name}" ps --format '{{.Name}}' 2>/dev/null | grep -q .; then
    echo "==> Stopping devcontainer stack '${project_name}'"
    docker compose -p "${project_name}" down --remove-orphans 2>&1 | sed 's/^/    /' || true
  else
    echo "==> No running stack for project '${project_name}'"
  fi
fi

# 3. Terminate EVG host by displayName.
if [[ ${keep_evg_host} -eq 0 ]]; then
  evg_query_cli=""
  for candidate in \
    "${MCK_DEV_PLUGIN_ROOT:-${HOME}/mdb/core-platforms-ai-tools/plugins/mck-dev}/scripts/evg-query" \
    "${HOME}/.claude/plugins/cache/core-platforms-ai-tools/mck-dev"/*/scripts/evg-query
  do
    [[ -x "${candidate}" ]] && evg_query_cli="${candidate}" && break
  done
  if [[ -n "${evg_query_cli}" ]] && command -v evergreen >/dev/null 2>&1; then
    host_id="$("${evg_query_cli}" get_my_hosts 2>/dev/null \
      | jq -r --arg name "${evg_host_name}" \
          '[.[] | select(.displayName == $name and (.status | ascii_downcase | IN("terminated","decommissioned") | not))] | first | .id // empty')"
    if [[ -n "${host_id}" ]]; then
      echo "==> Terminating EVG host '${evg_host_name}' (${host_id})"
      evergreen host terminate --host "${host_id}" 2>&1 | sed 's/^/    /' || true
    else
      echo "==> No running EVG host with displayName '${evg_host_name}'"
    fi
  else
    echo "==> Skipping EVG host termination (evg-query or evergreen CLI not available)"
  fi
fi

# 4. Remove the git worktree.
if [[ ${keep_worktree} -eq 0 ]]; then
  if [[ -d "${worktree_path}" ]]; then
    echo "==> Removing git worktree at ${worktree_path}"
    git -C "${src_repo_root}" worktree remove --force "${worktree_path}" 2>&1 | sed 's/^/    /' || \
      echo "    (worktree remove failed — directory may need manual cleanup)"
  else
    echo "==> Worktree dir ${worktree_path} doesn't exist; skipping"
  fi
  git -C "${src_repo_root}" worktree prune 2>&1 | sed 's/^/    /' || true
fi

# 5. Optionally delete the branch.
if [[ ${delete_branch} -eq 1 ]]; then
  if git -C "${src_repo_root}" show-ref --verify --quiet "refs/heads/${branch}"; then
    echo "==> Deleting local branch '${branch}'"
    git -C "${src_repo_root}" branch -D "${branch}" 2>&1 | sed 's/^/    /' || true
  else
    echo "==> Branch '${branch}' doesn't exist locally; skipping"
  fi
fi

echo "==> teardown: done"
