#!/usr/bin/env bash
# shellcheck disable=SC2086,SC2250

set -euo pipefail

source scripts/dev/set_env_context.sh

delete_project() {
  project_id=$1
  if [[ "${project_id}" != "" && "${project_id}" != "null" ]]; then
    echo "Removing controlledFeature policies for project (${project_id})"
    curl -X PUT --digest -u "${OM_USER}:${OM_API_KEY}" "${OM_HOST}/api/public/v1.0/groups/${project_id}/controlledFeature" -H 'Content-Type: application/json' -d '{"externalManagementSystem": {"name": "mongodb-enterprise-operator"},"policies": []}'
    echo
    echo "Removing any existing automationConfig for project (${project_id})"
    curl -X PUT --digest -u "${OM_USER}:${OM_API_KEY}" "${OM_HOST}/api/public/v1.0/groups/${project_id}/automationConfig" -H 'Content-Type: application/json' -d '{}'
    echo
    echo "Deleting project (${project_id})"
    curl -X DELETE --digest -u "${OM_USER}:${OM_API_KEY}" "${OM_HOST}/api/public/v1.0/groups/${project_id}"
    echo
  else
    echo "Project ${project_id} is already deleted"
  fi
}

delete_all_projects() {
  for project_id in $(curl -s -u "${OM_USER}:${OM_API_KEY}" --digest "${OM_HOST}/api/public/v1.0/groups" | jq -r ".results.[].id"); do
    delete_project $project_id &
  done
  wait
}

delete_all_projects
#delete_project "6940439eaa73c646422c2607"
#delete_project "6940198b44fa6628630bc339"
#delete_project "694142c26906260a58bcf015"
