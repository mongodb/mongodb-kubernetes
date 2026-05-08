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
# If MCK_DEVC_NET_PREFIX is already set, validate it's a number in [16,31]
# and trust the caller. Otherwise scan docker networks to find a free X.

set -Eeou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

valid_range_lo=16
valid_range_hi=31

if [[ -n "${MCK_DEVC_NET_PREFIX:-}" ]]; then
  # Trust the caller's choice if it parses as a valid prefix.
  if [[ "${MCK_DEVC_NET_PREFIX}" =~ ^[0-9]+$ \
        && "${MCK_DEVC_NET_PREFIX}" -ge ${valid_range_lo} \
        && "${MCK_DEVC_NET_PREFIX}" -le ${valid_range_hi} ]]; then
    echo "MCK_DEVC_NET_PREFIX=${MCK_DEVC_NET_PREFIX}"
    exit 0
  fi
  echo "ERROR: MCK_DEVC_NET_PREFIX='${MCK_DEVC_NET_PREFIX}' is not in [${valid_range_lo}, ${valid_range_hi}]" >&2
  exit 1
fi

# Collect all 172.X.* subnets currently in use across docker networks.
used_prefixes=()
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
  local cand="$1"
  local p
  for p in "${used_prefixes[@]}"; do
    [[ "${p}" == "${cand}" ]] && return 0
  done
  return 1
}

# Walk the valid range and emit the first free prefix.
for x in $(seq ${valid_range_lo} ${valid_range_hi}); do
  if ! is_used "${x}"; then
    echo "MCK_DEVC_NET_PREFIX=${x}"
    exit 0
  fi
done

echo "ERROR: no free 172.[${valid_range_lo}-${valid_range_hi}].0.0/16 subnet available." >&2
echo "Used prefixes: ${used_prefixes[*]}" >&2
exit 1
