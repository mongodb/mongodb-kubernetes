#!/usr/bin/env bash
#
# Pick a free 172.X.0.0/16 subnet for this worktree's devcontainer compose
# stack and print `MCK_DEVC_NET_PREFIX=X` (numeric second octet only) so the
# caller can write it to .devcontainer/.env (or export it).
#
# Why: compose.yml hardcodes the 172.28 prefix, which collides whenever a
# second worktree's stack is started. Choose a free X in 172.[16-31].x so
# multiple stacks coexist on the same host.
#
# Concurrency: parallel orchestration runs racing on the docker-only "free"
# scan would both pick the same prefix, then collide on `compose up`. To
# avoid that we keep a host-local registry of branch_dir → prefix
# assignments at ~/.cache/mck-devc/net-prefix-registry, guarded by a
# mkdir-based lock (portable across macOS and Linux — flock is Linux-only).
# Reservation is recorded BEFORE the docker network exists, so concurrent
# selectors see each other.
#
# Usage:
#   dc_select_network.sh [--branch-dir <name>]
#   dc_select_network.sh --free <branch-dir>
#
# If --branch-dir is provided and already has an entry in the registry,
# the existing prefix is returned (idempotent for re-runs of the same
# worktree). Otherwise a new prefix is chosen and recorded.
#
# `--free <branch-dir>` removes the entry for that branch from the
# registry under the same lock. Used by `wt_teardown.sh` to release the
# slot when a worktree is torn down. No-op if the entry isn't there.
#
# If MCK_DEVC_NET_PREFIX is already set, validate it's a number in [16,31]
# and trust the caller (skip both the registry and the docker scan).

set -Eeou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

valid_range_lo=16
valid_range_hi=31

branch_dir=""
free_branch_dir=""
while [[ $# -gt 0 ]]; do
  case $1 in
    --branch-dir) branch_dir="$2"; shift 2 ;;
    --free)       free_branch_dir="$2"; shift 2 ;;
    *) echo "Unknown arg: $1" >&2; exit 2 ;;
  esac
done

if [[ -n "${branch_dir}" && -n "${free_branch_dir}" ]]; then
  echo "ERROR: --branch-dir and --free are mutually exclusive." >&2
  exit 2
fi

if [[ -n "${MCK_DEVC_NET_PREFIX:-}" && -z "${free_branch_dir}" ]]; then
  if [[ "${MCK_DEVC_NET_PREFIX}" =~ ^[0-9]+$ \
        && "${MCK_DEVC_NET_PREFIX}" -ge ${valid_range_lo} \
        && "${MCK_DEVC_NET_PREFIX}" -le ${valid_range_hi} ]]; then
    echo "MCK_DEVC_NET_PREFIX=${MCK_DEVC_NET_PREFIX}"
    exit 0
  fi
  echo "ERROR: MCK_DEVC_NET_PREFIX='${MCK_DEVC_NET_PREFIX}' is not in [${valid_range_lo}, ${valid_range_hi}]" >&2
  exit 1
fi

registry_dir="${MCK_DEVC_REGISTRY_DIR:-${HOME}/.cache/mck-devc}"
registry_file="${registry_dir}/net-prefix-registry"
lock_dir="${registry_dir}/net-prefix-registry.lock.d"

mkdir -p "${registry_dir}"
touch "${registry_file}"

# Acquire lock via mkdir (atomic on POSIX). Retry up to ~30s.
deadline=$(( $(date +%s) + 30 ))
while ! mkdir "${lock_dir}" 2>/dev/null; do
  if [[ $(date +%s) -ge ${deadline} ]]; then
    echo "ERROR: could not acquire lock at ${lock_dir} within 30s. If stale, rmdir it." >&2
    exit 1
  fi
  sleep 0.2
done
trap 'rmdir "${lock_dir}" 2>/dev/null || true' EXIT

# --free mode: drop the entry for the given branch_dir and exit.
# Done under the same lock so it can't race with a concurrent allocator.
if [[ -n "${free_branch_dir}" ]]; then
  tmp="$(mktemp "${registry_file}.XXXXXX")"
  awk -F= -v k="${free_branch_dir}" '$1!=k' "${registry_file}" > "${tmp}"
  mv "${tmp}" "${registry_file}"
  echo "freed: ${free_branch_dir}" >&2
  exit 0
fi

# Idempotent path: branch already has a prefix? Return it.
if [[ -n "${branch_dir}" ]]; then
  existing="$(awk -F= -v k="${branch_dir}" '$1==k {print $2; exit}' "${registry_file}" || true)"
  if [[ -n "${existing}" ]]; then
    echo "MCK_DEVC_NET_PREFIX=${existing}"
    exit 0
  fi
fi

# Collect used prefixes from BOTH the registry (authoritative for reservations
# made by other concurrent runs) and live docker networks (catches anything
# created out-of-band).
used_prefixes=()
while IFS= read -r p; do
  [[ -n "${p}" ]] && used_prefixes+=("${p}")
done < <(awk -F= 'NF==2 {print $2}' "${registry_file}")

while IFS= read -r subnet; do
  [[ -z "${subnet}" ]] && continue
  if [[ "${subnet}" =~ ^172\.([0-9]+)\. ]]; then
    used_prefixes+=("${BASH_REMATCH[1]}")
  fi
done < <(
  docker network ls --format '{{.Name}}' 2>/dev/null \
    | xargs -I{} docker network inspect {} \
        --format '{{range .IPAM.Config}}{{.Subnet}}{{"\n"}}{{end}}' \
        2>/dev/null
)

is_used() {
  local cand="$1" p
  for p in "${used_prefixes[@]}"; do
    [[ "${p}" == "${cand}" ]] && return 0
  done
  return 1
}

for x in $(seq ${valid_range_lo} ${valid_range_hi}); do
  if ! is_used "${x}"; then
    if [[ -n "${branch_dir}" ]]; then
      printf '%s=%s\n' "${branch_dir}" "${x}" >> "${registry_file}"
    fi
    echo "MCK_DEVC_NET_PREFIX=${x}"
    exit 0
  fi
done

echo "ERROR: no free 172.[${valid_range_lo}-${valid_range_hi}].0.0/16 subnet available." >&2
echo "Used prefixes: ${used_prefixes[*]}" >&2
exit 1
