#!/usr/bin/env bash

###
# This is a cleanup script for preparing cloud-qa to e2e run.
# It deletes all projects that has been created in previous runs.
###

set -euo pipefail

test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

_om_curl() {
  # Silent + show-errors, fail on HTTP >=400, drop response body. Pin a
  # generous timeout so a slow cloud-qa can't hang prepare-local-e2e.
  curl -sS --digest -u "${OM_USER}:${OM_API_KEY}" \
    --fail --max-time 30 --retry 2 --retry-delay 2 \
    -o /dev/null "$@"
}

delete_project() {
  local project_name=$1
  echo "Deleting project id of ${project_name} from ${OM_HOST}"
  local project_id
  project_id=$(curl -sS --digest -u "${OM_USER}:${OM_API_KEY}" \
    --max-time 30 --retry 2 --retry-delay 2 \
    "${OM_HOST}/api/public/v1.0/groups/byName/${project_name}" 2>/dev/null \
    | jq -r 'if type == "object" then (.id // "") else "" end' 2>/dev/null) || project_id=""
  if [[ "${project_id}" != "" && "${project_id}" != "null" ]]; then
    echo "  controlledFeature → reset (${project_id})"
    _om_curl -X PUT "${OM_HOST}/api/public/v1.0/groups/${project_id}/controlledFeature" \
      -H 'Content-Type: application/json' \
      -d '{"externalManagementSystem": {"name": "mongodb-enterprise-operator"},"policies": []}'
    echo "  automationConfig  → reset (${project_id})"
    _om_curl -X PUT "${OM_HOST}/api/public/v1.0/groups/${project_id}/automationConfig" \
      -H 'Content-Type: application/json' -d '{}'
    echo "  group             → delete (${project_id})"
    _om_curl -X DELETE "${OM_HOST}/api/public/v1.0/groups/${project_id}"
  else
    echo "  already deleted"
  fi
}

# Filter a groups-list JSON (read on stdin) to the project names that should be
# deleted for the given base prefix. Pure (no network) so it can be unit-tested.
#
# Worktree safety: parallel devc stacks derive their namespace as
# <base-namespace>-<MCK_DEVC_NET_PREFIX>, where the net prefix is numeric — so
# every sibling worktree's projects live under "<base>-<digits>[-...]". A base
# checkout (NAMESPACE=mongodb-test) must NOT prefix-delete those, or it wipes
# the OM projects of its own other worktree stacks. We therefore skip any
# candidate whose first path segment after the prefix is purely numeric;
# resource-specific projects (non-numeric first segment) are still returned.
filter_prefixed_projects() {
  local prefix=$1
  # Tolerate a non-object body (a bare number, an error page, an unexpected
  # shape): only descend into .results when the top level is actually an
  # object, otherwise emit nothing. Keeps teardown quiet when cloud-qa returns
  # something other than a groups list.
  jq -r --arg p "${prefix}-" '
      (if type == "object" then .results else empty end)
      | .[]?
      | select(.name | startswith($p))
      | select((.name | ltrimstr($p) | split("-")[0] | test("^[0-9]+$")) | not)
      | .name'
}

# Delete resource-specific projects (e.g. mongodb-test-mdb-sh) that the operator
# creates when a MongoDB CR uses a custom project name. Without this, stale
# automation configs persist in Cloud Manager across test runs, causing cert
# hash mismatches that deadlock the agent. The worktree's own exact namespace
# is cleaned separately by the exact-name delete in main().
delete_projects_with_prefix() {
  local prefix=$1
  echo "Listing projects with prefix '${prefix}-' to clean up (excluding sibling worktree stacks)"
  local response projects
  # --fail: on HTTP >=400 curl exits non-zero and emits nothing, so an error
  # body never reaches jq. Any failure (network, auth, non-JSON) degrades to
  # "nothing to clean" rather than aborting teardown.
  if ! response=$(curl -sS --digest -u "${OM_USER}:${OM_API_KEY}" \
    --fail --max-time 30 --retry 2 --retry-delay 2 \
    "${OM_HOST}/api/public/v1.0/groups?itemsPerPage=100" 2>/dev/null); then
    echo "  (could not list projects; skipping prefix cleanup)"
    return 0
  fi
  projects=$(printf '%s' "${response}" | filter_prefixed_projects "${prefix}" 2>/dev/null) || true
  if [[ -z "${projects}" ]]; then
    echo "No prefixed projects found"
    return
  fi
  for project_name in ${projects}; do
    delete_project "${project_name}" || true
  done
}

main() {
  source scripts/dev/set_env_context.sh

  delete_project "${NAMESPACE}"
  delete_projects_with_prefix "${NAMESPACE}"

  if [[ "${WATCH_NAMESPACE:-}" != "" && "${WATCH_NAMESPACE:-}" != "*" ]]; then
    for ns in ${WATCH_NAMESPACE/,// }; do
      delete_project "${ns}" || true
      delete_projects_with_prefix "${ns}" || true
    done
  fi
}

# Execute only when run directly; sourcing (e.g. from bats) exposes the pure
# helpers for unit testing without touching the OM API.
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  main "$@"
fi
