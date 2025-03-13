#!/usr/bin/env bash
PROJECT_NAME="${1}"
echo "PROJECT_NAME=${PROJECT_NAME}"
PROJECT_ID=$(mongocli iam project list --output=json | jq --arg name "${PROJECT_NAME}" -r '.results[] | select(.name==$name) | .id')
CLUSTER_NAME="${2:-none}"
if [[ "${CLUSTER_NAME}" == "none" ]]; then
    CLUSTER_INFO=$(mongocli atlas clusters list --projectId "${PROJECT_ID}" --output=json | jq '.[0]')
else
    CLUSTER_INFO=$(mongocli atlas clusters list --projectsId "${PROJECT_ID}" --output=json | jq --arg name "${CLUSTER_NAME}" -r '.[] | select(.name==$name) | .')
fi
echo "CLUSTER_INFO=${CLUSTER_INFO}"


echo "\
apiVersion: v1
kind: Secret
metadata:
  name: green-db-admin-admin
  namespace: community
type: Opaque
data:
  connectionString.standard: ${CLUSTER_INFO}
  connectionString.standardSrv: bW9uZ29kYitzcnY6Ly9hZG1pbjp0ZXN0aW5nMTIzNCUyMUBncmVlbi1kYi1zdmMuY29tbXVuaXR5LnN2Yy5jbHVzdGVyLmxvY2FsL2FkbWluP3NzbD1mYWxzZQ==
  password: dGVzdGluZzEyMzQh
  username: YWRtaW4=
"
