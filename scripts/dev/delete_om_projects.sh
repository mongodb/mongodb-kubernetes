#!/usr/bin/env bash

###
# This is a cleanup script for preparing cloud-qa to e2e run.
# It deletes all projects that has been created in previous runs.
###

set -euo pipefail

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

if [[ "${WATCH_NAMESPACE:-}" != "" && "${WATCH_NAMESPACE:-}" != "*" ]]; then
  for ns in ${WATCH_NAMESPACE/,// }; do
    if [[ "${ns}" != "${NAMESPACE}" ]]; then
      delete_project "${ns}" || true
    fi
  done
fi
