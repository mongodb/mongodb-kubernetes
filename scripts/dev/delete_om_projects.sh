#!/usr/bin/env bash

###
# This is a cleanup script for preparing cloud-qa to e2e run.
# It deletes all projects that has been created in previous runs.
###

set -euo pipefail

test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

source scripts/dev/set_env_context.sh

delete_project() {
  project_name=$1
  echo "Deleting project id of ${project_name} from ${OM_HOST}"
  project_id=$(curl -s -u "${OM_USER}:${OM_API_KEY}" --digest "${OM_HOST}/api/public/v1.0/groups/byName/${project_name}" | jq -r .id)
  if [[ "${project_id}" != "" && "${project_id}" != "null" ]]; then
    echo "Removing controlledFeature policies for project ${project_name} (${project_id})"
    curl -X PUT --digest -u "${OM_USER}:${OM_API_KEY}" "${OM_HOST}/api/public/v1.0/groups/${project_id}/controlledFeature" -H 'Content-Type: application/json' -d '{"externalManagementSystem": {"name": "mongodb-enterprise-operator"},"policies": []}'
    echo
    echo "Removing any existing automationConfig for project ${project_name} (${project_id})"
    curl -X PUT --digest -u "${OM_USER}:${OM_API_KEY}" "${OM_HOST}/api/public/v1.0/groups/${project_id}/automationConfig" -H 'Content-Type: application/json' -d '{}'
    echo
    echo "Deleting project ${project_name} (${project_id})"
    curl -X DELETE --digest -u "${OM_USER}:${OM_API_KEY}" "${OM_HOST}/api/public/v1.0/groups/${project_id}"
    echo
  else
    echo "Project ${project_name} is already deleted"
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
  projects=$(curl -s -u "${OM_USER}:${OM_API_KEY}" --digest \
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
