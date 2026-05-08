#!/usr/bin/env bash
#
# One-shot cleanup of legacy `ls` / `ls-*` cloud-qa OM projects.
#
# Background. Before per-worktree namespace isolation landed, every devcontainer
# worktree on the same host defaulted to NAMESPACE=ls. Their cloud-qa OM
# projects were named `ls`, `ls-mdb-foo`, etc. Parallel worktrees collided and
# delete_om_projects.sh torched each other's projects. After the isolation fix,
# new projects are namespaced under `ls-<digit>-...` (where the digit is
# MCK_DEVC_NET_PREFIX, 16..31). This script deletes the legacy non-namespaced
# `ls` / `ls-{non-digit-prefixed}-...` projects that don't match the new
# pattern, so cloud-qa stops accumulating orphans.
#
# *** RUN ONCE, MANUALLY, ON A QUIET DAY. ***
# This is a one-shot, not part of automation. It does NOT delete `ls-16-*` or
# any other in-use per-worktree project. It only removes legacy orphans.
#
# Usage:
#   bash scripts/dev/cleanup_legacy_ls_om_projects.sh           # dry-run (default)
#   bash scripts/dev/cleanup_legacy_ls_om_projects.sh --apply   # actually delete
#
# Requires: OM_USER, OM_API_KEY, OM_HOST in env (e.g. via `make switch`).

set -Eeou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

source scripts/dev/set_env_context.sh

apply=0
case "${1:-}" in
  --apply) apply=1 ;;
  ""|--dry-run) apply=0 ;;
  -h|--help) sed -n '3,22p' "$0"; exit 0 ;;
  *) echo "Unknown option: $1" >&2; sed -n '3,22p' "$0"; exit 1 ;;
esac

if [[ -z "${OM_USER:-}" || -z "${OM_API_KEY:-}" || -z "${OM_HOST:-}" ]]; then
  echo "ERROR: OM_USER, OM_API_KEY, OM_HOST must be set in env." >&2
  exit 1
fi

echo "==> Listing all cloud-qa OM projects (this may take a moment)..."
all_projects="$(curl -s -u "${OM_USER}:${OM_API_KEY}" --digest \
  "${OM_HOST}/api/public/v1.0/groups?itemsPerPage=500" \
  | jq -r '.results[]?.name' | sort -u)"

# Legacy pattern: name == "ls" OR (name starts with "ls-" AND second segment is
# NOT a 2-digit number in [16,31]). The new per-worktree pattern is
# `ls-<digit>...` where the digit is one of 16..31 (MCK_DEVC_NET_PREFIX range).
legacy="$(echo "${all_projects}" \
  | awk '
      $0 == "ls" { print; next }
      /^ls-/ {
        # peel off the "ls-" prefix and inspect the next segment up to the
        # next "-" or end-of-string
        rest = substr($0, 4)
        n = index(rest, "-")
        if (n == 0) { seg = rest } else { seg = substr(rest, 1, n - 1) }
        if (seg ~ /^[0-9]+$/ && seg+0 >= 16 && seg+0 <= 31) {
          # new-pattern, in-use — skip
          next
        }
        print
      }')"

if [[ -z "${legacy}" ]]; then
  echo "==> No legacy ls / ls-* projects found. Nothing to do."
  exit 0
fi

count="$(printf '%s\n' "${legacy}" | wc -l | tr -d ' ')"
echo "==> Found ${count} legacy project(s):"
# shellcheck disable=SC2086  # intentional word-splitting on newlines
printf '  %s\n' ${legacy}

if [[ ${apply} -eq 0 ]]; then
  echo
  echo "DRY-RUN. Re-run with --apply to actually delete these projects."
  exit 0
fi

delete_project() {
  local project_name="$1"
  echo "==> Deleting '${project_name}'"
  local project_id
  project_id=$(curl -s -u "${OM_USER}:${OM_API_KEY}" --digest \
    "${OM_HOST}/api/public/v1.0/groups/byName/${project_name}" | jq -r .id)
  if [[ -z "${project_id}" || "${project_id}" == "null" ]]; then
    echo "    (already deleted)"
    return
  fi
  curl -X PUT --digest -u "${OM_USER}:${OM_API_KEY}" \
    "${OM_HOST}/api/public/v1.0/groups/${project_id}/controlledFeature" \
    -H 'Content-Type: application/json' \
    -d '{"externalManagementSystem": {"name": "mongodb-enterprise-operator"},"policies": []}' \
    >/dev/null 2>&1 || true
  curl -X PUT --digest -u "${OM_USER}:${OM_API_KEY}" \
    "${OM_HOST}/api/public/v1.0/groups/${project_id}/automationConfig" \
    -H 'Content-Type: application/json' -d '{}' \
    >/dev/null 2>&1 || true
  curl -X DELETE --digest -u "${OM_USER}:${OM_API_KEY}" \
    "${OM_HOST}/api/public/v1.0/groups/${project_id}" \
    >/dev/null 2>&1 || true
}

while IFS= read -r name; do
  [[ -z "${name}" ]] && continue
  delete_project "${name}" || echo "    (delete failed for '${name}', continuing)"
done <<<"${legacy}"

echo "==> Done."
