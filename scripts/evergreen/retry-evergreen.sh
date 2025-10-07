#!/usr/bin/env bash

set -Eeou pipefail

###
## This script automatically retriggers failed tasks from Evergreen based on the `version_id` passed in from Evergreen.
##
## usage:
##   Set EVERGREEN_USER and EVERGREEN_API_KEY env. variables
##   Obtain the version from either Evergreen UI or Github checks
##   Call
##     ./retry-evergreen.sh 62cfba5957e85a64e1f801fa
###
echo "EVERGREEN_RETRY=${EVERGREEN_RETRY:-"true"}"
if [[ "${EVERGREEN_RETRY:-"true"}" != "true" ]]; then
  echo "Skipping evergreen retry"
  exit 0
fi

if [ $# -eq 0 ]; then
  echo "Details URL not passed in, exiting..."
  exit 1
else
  VERSION=$1
fi
if [ -z "${EVERGREEN_USER}" ]; then
  echo "$$EVERGREEN_USER not set"
  exit 1
fi
if [ -z "${EVERGREEN_API_KEY}" ]; then
  echo "$$EVERGREEN_API_KEY not set"
  exit 1
fi

EVERGREEN_API="https://evergreen.mongodb.com/api"
MAX_RETRIES=1

# Define build variants to exclude
# We ignore openshift because they only run one test at a time.
# Evergreen makes them dependant on each other, meaning if one gets restarted the whole suite gets restarted.
EXCLUDE_VARIANTS=("unit_tests" "run_pre_commit" "e2e_mdb_openshift_ubi_cloudqa" "e2e_openshift_static_mdb_ubi_cloudqa")
EXCLUDE_JQ_FILTER=$(printf '.build_variant != "%s" and ' "${EXCLUDE_VARIANTS[@]}" | sed 's/ and $//')

# shellcheck disable=SC2207
BUILD_IDS=($(curl -s -H "Api-User: ${EVERGREEN_USER}" -H "Api-Key: ${EVERGREEN_API_KEY}" \
            ${EVERGREEN_API}/rest/v2/versions/"${VERSION}" | \
            jq -r ".build_variants_status[] | select(${EXCLUDE_JQ_FILTER}) | .build_id"))

for BUILD_ID in "${BUILD_IDS[@]}"; do
  echo "Finding failed tasks in BUILD ID: ${BUILD_ID}"
  # shellcheck disable=SC2207
  TASK_IDS=($(curl -s -H "Api-User: ${EVERGREEN_USER}" -H "Api-Key: ${EVERGREEN_API_KEY}" ${EVERGREEN_API}/rest/v2/builds/"${BUILD_ID}"/tasks | jq ".[] | select(.status == \"failed\" and .execution < ${MAX_RETRIES})" | jq -r '.task_id'))

  for TASK_ID in "${TASK_IDS[@]}"; do
    echo "Retriggering TASK ID: ${TASK_ID}"
    curl -H "Api-User: ${EVERGREEN_USER}" -H "Api-Key: ${EVERGREEN_API_KEY}" -X POST ${EVERGREEN_API}/rest/v2/tasks/"${TASK_ID}"/restart
  done
done
