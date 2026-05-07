#!/usr/bin/env bash

###
# This is a cleanup script for preparing cloud-qa to e2e run.
# It deletes all projects that has been created in previous runs.
###

set -euo pipefail

test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

source scripts/dev/set_env_context.sh

# Per-worktree OM project name isolation (mirrors root-context derivation).
#
# When MCK_DEVC_NET_PREFIX is set (devcontainer-mode worktree dev), suffix
# NAMESPACE with the prefix so the cleanup filter only matches this worktree's
# `${NAMESPACE}-${prefix}` / `${NAMESPACE}-${prefix}-*` projects. Without this
# mirror the script would delete every parallel worktree's OM projects, which
# is the bug this PR fixes. The same logic lives in root-context so `make
# switch` writes the suffixed NAMESPACE into .generated/context.env;
# this local copy lets host-side callers (e.g. wt_teardown.sh, or any rerun
# made before the worktree context is refreshed) delete the right scope.
if [[ -z "${MCK_DEVC_NET_PREFIX:-}" && -f "${PROJECT_DIR:-.}/.devcontainer/.env" ]]; then
  devc_prefix_line="$(grep '^MCK_DEVC_NET_PREFIX=' "${PROJECT_DIR:-.}/.devcontainer/.env" 2>/dev/null | tail -n1 || true)"
  if [[ -n "${devc_prefix_line}" ]]; then
    export "${devc_prefix_line?}"
  fi
  unset devc_prefix_line
fi
if [[ -n "${MCK_DEVC_NET_PREFIX:-}" && -n "${NAMESPACE:-}" \
      && "${NAMESPACE}" != *"-${MCK_DEVC_NET_PREFIX}" ]]; then
  NAMESPACE="${NAMESPACE}-${MCK_DEVC_NET_PREFIX}"
  WATCH_NAMESPACE="${WATCH_NAMESPACE:-${NAMESPACE}}"
fi

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
    "${OM_HOST}/api/public/v1.0/groups/byName/${project_name}" | jq -r .id)
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

delete_project "${NAMESPACE}"

# Delete resource-specific projects (e.g. mongodb-test-mdb-sh) that the operator
# creates when a MongoDB CR uses a custom project name. Without this, stale
# automation configs persist in Cloud Manager across test runs, causing cert
# hash mismatches that deadlock the agent.
delete_projects_with_prefix() {
  local prefix=$1
  echo "Listing projects with prefix '${prefix}-' to clean up"
  local projects
  projects=$(curl -sS --digest -u "${OM_USER}:${OM_API_KEY}" \
    --max-time 30 --retry 2 --retry-delay 2 \
    "${OM_HOST}/api/public/v1.0/groups?itemsPerPage=100" \
    | jq -r ".results[]? | select(.name | startswith(\"${prefix}-\")) | .name") || true
  if [[ -z "${projects}" ]]; then
    echo "No prefixed projects found"
    return
  fi
  for project_name in ${projects}; do
    delete_project "${project_name}" || true
  done
}

delete_projects_with_prefix "${NAMESPACE}"

if [[ "${WATCH_NAMESPACE:-}" != "" && "${WATCH_NAMESPACE:-}" != "*" ]]; then
  for ns in ${WATCH_NAMESPACE/,// }; do
    delete_project "${ns}" || true
    delete_projects_with_prefix "${ns}" || true
  done
fi
