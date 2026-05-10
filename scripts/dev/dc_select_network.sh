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
#   dc_select_network.sh [--branch-dir <name>]   # allocate or return existing
#   dc_select_network.sh --list                  # show registry + status
#   dc_select_network.sh --prune [--dry-run]     # GC stale registry entries
#   dc_select_network.sh --release <branch_dir>  # remove a specific entry
#
# Allocation:
#   If --branch-dir is provided and already has an entry in the registry,
#   the existing prefix is returned (idempotent). Otherwise a new prefix
#   is chosen and recorded.
#   If MCK_DEVC_NET_PREFIX is already set, validate it's a number in
#   [16,31] and trust the caller (skip both the registry and the docker
#   scan).
#
# Stale detection (used by --list and --prune):
#   A registry entry "<branch_dir>=<prefix>" is stale when the worktree
#   directory <repo_parent>/<branch_dir> doesn't exist (git worktree was
#   removed). The script never deletes the live docker network — only
#   the registry record. If a stale registry entry's docker network is
#   still around (orphan compose stack), --prune --networks will offer
#   to remove it; otherwise leave it alone.

set -Eeou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

valid_range_lo=16
valid_range_hi=31

mode="allocate"
branch_dir=""
release_target=""
dry_run=0
prune_networks=0
while [[ $# -gt 0 ]]; do
  case $1 in
    --branch-dir) branch_dir="$2"; shift 2 ;;
    --list)       mode="list"; shift ;;
    --prune)      mode="prune"; shift ;;
    --release)    mode="release"; release_target="$2"; shift 2 ;;
    --dry-run)    dry_run=1; shift ;;
    --networks)   prune_networks=1; shift ;;
    -h|--help)    sed -n '3,40p' "$0"; exit 0 ;;
    *) echo "Unknown arg: $1" >&2; exit 2 ;;
  esac
done

registry_dir="${MCK_DEVC_REGISTRY_DIR:-${HOME}/.cache/mck-devc}"
registry_file="${registry_dir}/net-prefix-registry"
lock_dir="${registry_dir}/net-prefix-registry.lock.d"

mkdir -p "${registry_dir}"
touch "${registry_file}"

acquire_lock() {
  local deadline
  deadline=$(( $(date +%s) + 30 ))
  while ! mkdir "${lock_dir}" 2>/dev/null; do
    if [[ $(date +%s) -ge ${deadline} ]]; then
      echo "ERROR: could not acquire lock at ${lock_dir} within 30s. If stale, rmdir it." >&2
      exit 1
    fi
    sleep 0.2
  done
  trap 'rmdir "${lock_dir}" 2>/dev/null || true' EXIT
}

# Resolve the parent directory that wt_setup.sh / create_worktree.sh use as
# the worktree root. Sibling-of-this-repo by convention.
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
src_repo_root="$(cd "${script_dir}/../.." && pwd)"
worktree_parent="$(cd "${src_repo_root}/.." && pwd)"

# Returns 0 if the registry entry for ${1}=branch_dir is stale (no worktree
# directory of that name exists at the worktree parent). Sibling worktrees
# may live under ANY parent (some users use ~/mdb/<branch>, others use a
# different layout), so we check both `git worktree list` membership and
# the conventional sibling path. Stale only when BOTH miss.
is_stale_branch_dir() {
  local bd="$1"
  if [[ -d "${worktree_parent}/${bd}" ]]; then
    return 1
  fi
  if git -C "${src_repo_root}" worktree list --porcelain 2>/dev/null \
       | awk '/^worktree /{print $2}' \
       | xargs -I{} basename {} \
       | grep -qx "${bd}"; then
    return 1
  fi
  return 0
}

# Find docker networks whose subnet sits in 172.[16-31].0.0/16 and return
# "<network_name> <prefix>" pairs.
docker_networks_in_range() {
  docker network ls --format '{{.Name}}' 2>/dev/null \
    | while read -r net; do
        subnet=$(docker network inspect "${net}" \
                   --format '{{range .IPAM.Config}}{{.Subnet}}{{end}}' \
                   2>/dev/null || true)
        if [[ "${subnet}" =~ ^172\.([0-9]+)\. ]]; then
          local x="${BASH_REMATCH[1]}"
          if (( x >= valid_range_lo && x <= valid_range_hi )); then
            printf '%s\t%s\n' "${net}" "${x}"
          fi
        fi
      done
}

cmd_list() {
  acquire_lock
  echo "Registry: ${registry_file}"
  echo "Worktree parent (conventional): ${worktree_parent}"
  echo
  printf '%-55s  %-6s  %-6s  %s\n' "BRANCH_DIR" "PREFIX" "STATUS" "WORKTREE"
  printf '%-55s  %-6s  %-6s  %s\n' "----------" "------" "------" "--------"
  local stale=0 active=0
  while IFS='=' read -r bd p; do
    [[ -z "${bd}" || -z "${p}" ]] && continue
    if is_stale_branch_dir "${bd}"; then
      printf '%-55s  %-6s  %-6s  %s\n' "${bd}" "${p}" "stale" "(missing)"
      stale=$((stale+1))
    else
      printf '%-55s  %-6s  %-6s  %s\n' "${bd}" "${p}" "active" "${worktree_parent}/${bd}"
      active=$((active+1))
    fi
  done < "${registry_file}"
  echo
  echo "Summary: ${active} active, ${stale} stale (run with --prune to GC)."
  echo

  echo "Docker networks in 172.[${valid_range_lo}-${valid_range_hi}].0.0/16:"
  printf '%-65s  %s\n' "NETWORK" "PREFIX"
  printf '%-65s  %s\n' "-------" "------"
  docker_networks_in_range | sort -k2,2n -t$'\t' | while IFS=$'\t' read -r net x; do
    printf '%-65s  %s\n' "${net}" "${x}"
  done
  echo
  echo "Free range: 172.[${valid_range_lo}-${valid_range_hi}].0.0/16."
  echo "Used by registry + docker networks listed above are blocked."
  echo
  echo "Pruning info:"
  echo "  - Stale registry entries:  $0 --prune [--dry-run]"
  echo "  - Plus orphan docker nets: $0 --prune --networks [--dry-run]"
  echo "  - Specific entry:          $0 --release <branch_dir>"
  echo "  - When you tear down a worktree via wt_teardown.sh, the registry"
  echo "    entry is released automatically."
}

cmd_release() {
  if [[ -z "${release_target}" ]]; then
    echo "ERROR: --release requires a branch_dir argument" >&2
    exit 1
  fi
  acquire_lock
  if grep -q "^${release_target}=" "${registry_file}"; then
    local p
    p="$(awk -F= -v k="${release_target}" '$1==k {print $2; exit}' "${registry_file}")"
    if [[ ${dry_run} -eq 1 ]]; then
      echo "[dry-run] would release ${release_target}=${p}"
    else
      tmp="$(mktemp)"
      grep -v "^${release_target}=" "${registry_file}" > "${tmp}" || true
      mv "${tmp}" "${registry_file}"
      echo "Released ${release_target}=${p}."
    fi
  else
    echo "No registry entry for ${release_target}; nothing to release."
  fi
}

cmd_prune() {
  acquire_lock
  local removed=0 keep=()
  echo "Scanning registry for stale entries…"
  while IFS='=' read -r bd p; do
    [[ -z "${bd}" || -z "${p}" ]] && continue
    if is_stale_branch_dir "${bd}"; then
      if [[ ${dry_run} -eq 1 ]]; then
        echo "[dry-run] stale: ${bd}=${p}"
      else
        echo "Pruning stale registry entry: ${bd}=${p}"
      fi
      removed=$((removed+1))
    else
      keep+=("${bd}=${p}")
    fi
  done < "${registry_file}"

  if [[ ${dry_run} -eq 0 && ${removed} -gt 0 ]]; then
    : > "${registry_file}"
    for line in "${keep[@]}"; do
      echo "${line}" >> "${registry_file}"
    done
  fi
  echo "Registry: ${removed} stale entries $([[ ${dry_run} -eq 1 ]] && echo "would be" || echo "were") removed."

  if [[ ${prune_networks} -eq 1 ]]; then
    echo
    echo "Scanning docker networks for orphan devc compose stacks…"
    # A network is an "orphan" only when ALL hold:
    #   1. Name matches `<branch_dir>_devcontainer_devcontainer` (devc compose
    #      pattern). System networks (bridge, kind, k3d-mac, ...) are skipped
    #      regardless of attached-container count.
    #   2. 0 containers attached (compose stack down or never started).
    #   3. The implied branch has NO corresponding worktree (so this isn't
    #      a network for a live worktree whose user just happens to have
    #      stopped the compose stack temporarily).
    # docker network rm refuses on connected containers anyway, but we filter
    # ahead of the call to make --dry-run accurate.
    docker_networks_in_range | while IFS=$'\t' read -r net x; do
      case "${net}" in
        *_devcontainer_devcontainer) ;;
        *) continue ;;
      esac
      local containers
      containers="$(docker network inspect "${net}" --format '{{len .Containers}}' 2>/dev/null || echo 0)"
      [[ "${containers}" != "0" ]] && continue

      # Extract the project name (= lowercased branch_dir + "_devcontainer")
      # from the network name. Compose normalizes project names to lowercase,
      # so we match worktrees case-insensitively.
      local project_name="${net%_devcontainer}"
      local branch_lc="${project_name%_devcontainer}"
      local has_worktree=0
      while IFS= read -r wt; do
        wt_basename="$(basename "${wt}" | tr '[:upper:]' '[:lower:]')"
        if [[ "${wt_basename}" == "${branch_lc}" ]]; then
          has_worktree=1
          break
        fi
      done < <(git -C "${src_repo_root}" worktree list --porcelain 2>/dev/null \
                 | awk '/^worktree /{print $2}')

      if [[ ${has_worktree} -eq 1 ]]; then
        echo "Skipping ${net} (172.${x}.0.0/16): worktree '${branch_lc}' still exists. Use 'docker network rm ${net}' if you really want it gone."
        continue
      fi

      if [[ ${dry_run} -eq 1 ]]; then
        echo "[dry-run] orphan docker network: ${net} (172.${x}.0.0/16)"
      else
        echo "Removing orphan docker network: ${net} (172.${x}.0.0/16)"
        docker network rm "${net}" 2>&1 | sed 's/^/    /' || true
      fi
    done
  fi
}

cmd_allocate() {
  if [[ -n "${MCK_DEVC_NET_PREFIX:-}" ]]; then
    if [[ "${MCK_DEVC_NET_PREFIX}" =~ ^[0-9]+$ \
          && "${MCK_DEVC_NET_PREFIX}" -ge ${valid_range_lo} \
          && "${MCK_DEVC_NET_PREFIX}" -le ${valid_range_hi} ]]; then
      echo "MCK_DEVC_NET_PREFIX=${MCK_DEVC_NET_PREFIX}"
      exit 0
    fi
    echo "ERROR: MCK_DEVC_NET_PREFIX='${MCK_DEVC_NET_PREFIX}' is not in [${valid_range_lo}, ${valid_range_hi}]" >&2
    exit 1
  fi

  acquire_lock

  # Idempotent path: branch already has a prefix? Return it.
  if [[ -n "${branch_dir}" ]]; then
    existing="$(awk -F= -v k="${branch_dir}" '$1==k {print $2; exit}' "${registry_file}" || true)"
    if [[ -n "${existing}" ]]; then
      echo "MCK_DEVC_NET_PREFIX=${existing}"
      exit 0
    fi
  fi

  # Auto-prune stale entries before declaring exhaustion. This makes the
  # common case ("I just torn down a worktree, set up a new one") Just
  # Work without a separate --prune step. We only auto-prune; never auto-
  # remove docker networks (those may carry user state).
  pruned=0
  if [[ -s "${registry_file}" ]]; then
    keep=()
    while IFS='=' read -r bd p; do
      [[ -z "${bd}" || -z "${p}" ]] && continue
      if is_stale_branch_dir "${bd}"; then
        echo "Auto-pruning stale registry entry: ${bd}=${p}" >&2
        pruned=$((pruned+1))
      else
        keep+=("${bd}=${p}")
      fi
    done < "${registry_file}"
    if [[ ${pruned} -gt 0 ]]; then
      : > "${registry_file}"
      for line in "${keep[@]}"; do
        echo "${line}" >> "${registry_file}"
      done
    fi
  fi

  # Collect used prefixes from BOTH the registry (authoritative for
  # reservations made by other concurrent runs) and live docker networks
  # (catches anything created out-of-band).
  used_prefixes=()
  while IFS= read -r p; do
    [[ -n "${p}" ]] && used_prefixes+=("${p}")
  done < <(awk -F= 'NF==2 {print $2}' "${registry_file}")

  while IFS=$'\t' read -r _net x; do
    used_prefixes+=("${x}")
  done < <(docker_networks_in_range)

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

  cat >&2 <<EOM
ERROR: no free 172.[${valid_range_lo}-${valid_range_hi}].0.0/16 subnet available.
Used prefixes: ${used_prefixes[*]}

Diagnose / reclaim:
  $0 --list                          # show registry + docker networks
  $0 --prune --dry-run               # preview stale registry entries
  $0 --prune                         # remove stale registry entries
  $0 --prune --networks              # also remove orphan docker networks
  $0 --release <branch_dir>          # release a specific entry
EOM
  exit 1
}

case "${mode}" in
  allocate) cmd_allocate ;;
  list)     cmd_list ;;
  prune)    cmd_prune ;;
  release)  cmd_release ;;
  *) echo "ERROR: unknown mode ${mode}" >&2; exit 1 ;;
esac
