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
#   evg_prepare.sh [--multi] [--skip-recreate] [--name NAME] [--context CTX]
#   evg_prepare.sh --name dev-myfeature
#
# Options:
#   --name NAME       Display name to spawn / resume. Defaults to the worktree
#                     basename (after slash-to-underscore conversion).
#   --context CTX     Context to regenerate. Falls back to
#                     .generated/.current_context when omitted.
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
  sed -n '3,31p' "$0"
}

multi_cluster=0
skip_recreate=0
explicit_name=""
context_arg=""
distro=""
region=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --multi|--multi-cluster) multi_cluster=1; shift ;;
    --skip-recreate)         skip_recreate=1; shift ;;
    --name)                  explicit_name="$2"; shift 2 ;;
    --context)               context_arg="$2"; shift 2 ;;
    --distro)                distro="$2"; shift 2 ;;
    --region)                region="$2"; shift 2 ;;
    -h|--help)               usage; exit 0 ;;
    *) echo "Unknown argument: $1"; usage; exit 1 ;;
  esac
done

worktree_root="$(pwd)"
# Tooling-relative script location: the worktree at cwd may not carry
# wt-ctl / switch_context.sh / evg_host.sh (portable wt-ctl mode).
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
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
"${script_dir}/wt-ctl" --quiet evg spawn "${spawn_args[@]}"

# 2. Pin the host into this worktree's .generated/ so root-context picks it up.
mkdir -p "${worktree_root}/.generated"
echo -n "${evg_host_name}" > "${worktree_root}/.generated/.current-evg-host"

# 3. Re-run make switch so context*.env reflect the new EVG_HOST_NAME / ADDRESS.
#    Prefer the caller-supplied --context (the orchestrator knows it
#    authoritatively) over the .generated/.current_context sentinel, which a
#    concurrent create phase (dc_build's initializeCommand) can transiently
#    clear. make switch below regenerates .generated regardless.
current_context_file="${worktree_root}/.generated/.current_context"
if [[ -n "${context_arg}" ]]; then
  current_context="${context_arg}"
elif [[ -f "${current_context_file}" ]]; then
  current_context="$(cat "${current_context_file}")"
else
  echo "ERROR: no --context given and .generated/.current_context not found. Run 'make switch' once before this script." >&2
  exit 1
fi
echo "==> Regenerating context files (context=${current_context})"
PROJECT_DIR="${worktree_root}" "${script_dir}/switch_context.sh" "${current_context}"

# 4. Verify SSH connectivity through evg_host.sh.
echo "==> Verifying SSH to ${evg_host_name} via evg_host.sh"
if ! "${script_dir}/evg_host.sh" ssh -o ConnectTimeout=20 -o BatchMode=yes -- 'echo evg_host_ready'; then
  echo "ERROR: SSH check via evg_host.sh failed for ${evg_host_name}." >&2
  exit 1
fi

# 4b. Fail fast when no key in the forwarded ssh-agent is authorized on the
#     host. Step 4 succeeds via on-disk keys (~/.ssh/config), but the
#     devcontainer has no ~/.ssh mount and reaches the host only through
#     autossh + the forwarded agent. Without this check, kind recreation and
#     devc prepare run first and the first visible symptom is an apiserver
#     "Service Unavailable" ~10 minutes later. Set EVG_HOST_AGENT_CHECK=0 to
#     bypass (e.g. agentless setups).
if [[ "${EVG_HOST_AGENT_CHECK:-1}" != "0" ]]; then
  echo "==> Checking that the ssh-agent carries a key authorized on ${evg_host_name}"
  agent_fps="$(ssh-add -L 2>/dev/null | ssh-keygen -lf - 2>/dev/null | awk '{print $2}' | sort -u || true)"
  host_fps="$(
    "${script_dir}/evg_host.sh" ssh -o ConnectTimeout=20 -o BatchMode=yes -- 'cat ~/.ssh/authorized_keys 2>/dev/null' 2>/dev/null \
      | awk '{for (i = 1; i <= NF; i++) if ($i ~ /^(ssh-|ecdsa-|sk-)/) { print $i, $(i + 1); break }}' \
      | while read -r key; do printf '%s\n' "${key}" | ssh-keygen -lf - 2>/dev/null | awk '{print $2}'; done \
      | sort -u || true
  )"
  agent_authorized=0
  if [[ -n "${agent_fps}" && -n "${host_fps}" ]] \
      && comm -12 <(printf '%s\n' "${agent_fps}") <(printf '%s\n' "${host_fps}") | grep -q .; then
    agent_authorized=1
  fi
  if [[ ${agent_authorized} -ne 1 ]]; then
    echo "ERROR: no key in the ssh-agent is authorized on ${evg_host_name}." >&2
    echo "       The devcontainer reaches the EVG host only through the forwarded" >&2
    echo "       agent (it has no ~/.ssh mount), so bring-up would later fail with" >&2
    echo "       an apiserver 'Service Unavailable'." >&2
    echo "       Fix: ssh-add <the identity ~/.ssh/config uses for EVG hosts>" >&2
    echo "            e.g. ssh-add ~/.ssh/evg-host" >&2
    echo "       Then: wt-ctl create --resume <branch>" >&2
    exit 1
  fi
  echo "==> ssh-agent key check: ok"
fi

# 5. Recreate kind clusters unless explicitly suppressed.
if [[ ${skip_recreate} -eq 1 ]]; then
  echo "==> --skip-recreate set; skipping kind cluster recreation"
  echo "==> Refreshing kubeconfig from existing host"
  "${script_dir}/evg_host.sh" get-kubeconfig
elif [[ ${multi_cluster} -eq 1 ]]; then
  echo "==> Recreating all (multi) kind clusters on ${evg_host_name}"
  "${script_dir}/evg_host.sh" recreate-kind-clusters
else
  echo "==> Recreating single kind cluster on ${evg_host_name}"
  "${script_dir}/evg_host.sh" recreate-kind-cluster kind
fi

echo "==> evg_prepare: done — host=${evg_host_name}"
