#!/usr/bin/env bash
#
# Prepare an Evergreen host for use with this worktree.
#
# Behaviour:
#   1. Spawn (or resume — idempotent on displayName) a host named
#      ${EVG_HOST_NAME} via `wt-ctl evg spawn`.
#   2. Persist the host name into .generated/.current-evg-host so that
#      scripts/dev/contexts/root-context picks it up.
#   3. Re-run `make switch context=${current}` to regenerate context*.env
#      with EVG_HOST_NAME and (now) EVG_HOST_ADDRESS resolved.
#   4. Verify SSH via `scripts/dev/evg_host.sh ssh`.
#   5. Recreate kind clusters on the host (single by default; --multi for
#      the four-cluster setup; --skip-recreate to leave kind alone).
#
# Usage:
#   evg_prepare.sh [--multi] [--skip-recreate] [--name NAME]
#   evg_prepare.sh --name dev-myfeature
#
# Options:
#   --name NAME       Display name to spawn / resume. Defaults to the worktree
#                     basename (after slash-to-underscore conversion).
#   --multi           Recreate the four-cluster multi setup (e2e-operator,
#                     e2e-cluster-{1,2,3}, kind). Default is single (one
#                     `kind` cluster).
#   --skip-recreate   Don't recreate the kind cluster(s). Use to take over
#                     an already-prepared host without touching kind.
#   --distro DISTRO   evergreen distro for the spawn (default: evg spawn's own).
#   --region REGION   AWS region for the spawn (default: evg spawn's own).

set -Eeou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

usage() {
  sed -n '3,29p' "$0"
}

multi_cluster=0
skip_recreate=0
explicit_name=""
distro=""
region=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --multi|--multi-cluster) multi_cluster=1; shift ;;
    --skip-recreate)         skip_recreate=1; shift ;;
    --name)                  explicit_name="$2"; shift 2 ;;
    --distro)                distro="$2"; shift 2 ;;
    --region)                region="$2"; shift 2 ;;
    -h|--help)               usage; exit 0 ;;
    *) echo "Unknown argument: $1"; usage; exit 1 ;;
  esac
done

worktree_root="$(pwd)"
worktree_basename="$(basename "${worktree_root}")"

if [[ -n "${explicit_name}" ]]; then
  evg_host_name="${explicit_name}"
else
  evg_host_name="${worktree_basename}"
fi

echo "==> evg_prepare: worktree=${worktree_root}, host=${evg_host_name}"

# 1. Spawn (or resume) the host via wt-ctl's native EVG verb. Idempotent on
#    the Evergreen displayName; resumes any non-terminal host with that name.
echo "==> Spawning / resuming EVG host displayName='${evg_host_name}'"
spawn_args=(--name "${evg_host_name}")
[[ -n "${distro}" ]] && spawn_args+=(--distro "${distro}")
[[ -n "${region}" ]] && spawn_args+=(--region "${region}")
"${worktree_root}/scripts/dev/wt-ctl" --quiet evg spawn "${spawn_args[@]}"

# 2. Pin the host into this worktree's .generated/ so root-context picks it up.
mkdir -p "${worktree_root}/.generated"
echo -n "${evg_host_name}" > "${worktree_root}/.generated/.current-evg-host"

# 3. Re-run make switch so context*.env reflect the new EVG_HOST_NAME / ADDRESS.
current_context_file="${worktree_root}/.generated/.current_context"
if [[ ! -f "${current_context_file}" ]]; then
  echo "ERROR: .generated/.current_context not found. Run 'make switch' once before this script." >&2
  exit 1
fi
current_context="$(cat "${current_context_file}")"
echo "==> Regenerating context files (context=${current_context})"
make switch context="${current_context}"

# 4. Verify SSH connectivity through evg_host.sh.
echo "==> Verifying SSH to ${evg_host_name} via evg_host.sh"
if ! scripts/dev/evg_host.sh ssh -o ConnectTimeout=20 -o BatchMode=yes -- 'echo evg_host_ready'; then
  echo "ERROR: SSH check via evg_host.sh failed for ${evg_host_name}." >&2
  exit 1
fi

# 5. Recreate kind clusters unless explicitly suppressed.
if [[ ${skip_recreate} -eq 1 ]]; then
  echo "==> --skip-recreate set; skipping kind cluster recreation"
  echo "==> Refreshing kubeconfig from existing host"
  scripts/dev/evg_host.sh get-kubeconfig
elif [[ ${multi_cluster} -eq 1 ]]; then
  echo "==> Recreating all (multi) kind clusters on ${evg_host_name}"
  scripts/dev/evg_host.sh recreate-kind-clusters
else
  echo "==> Recreating single kind cluster on ${evg_host_name}"
  scripts/dev/evg_host.sh recreate-kind-cluster kind
fi

echo "==> evg_prepare: done — host=${evg_host_name}"
