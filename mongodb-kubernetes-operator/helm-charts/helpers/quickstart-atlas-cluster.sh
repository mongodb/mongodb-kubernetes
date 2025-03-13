#!/usr/bin/env bash
cluster_name="${1}"
ATLAS_PUBLIC_KEY=XXXX
ATLAS_PRIVATE_KEY=YYYY
ATLAS_ORG_ID=ZZZZ
helm install "${cluster_name}" \
    ./charts/atlas-cluster \
    --set atlas.publicApiKey="${ATLAS_PUBLIC_KEY}" \
    --set atlas.privateApiKey="${ATLAS_PRIVATE_KEY}" \
    --set atlas.orgId="${ATLAS_ORG_ID}" \
    --set project.name="${cluster_name}" \
    --set project.atlasProjectName="${cluster_name}" \
