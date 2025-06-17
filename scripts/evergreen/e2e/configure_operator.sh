#!/usr/bin/env bash
set -Eeou pipefail -o posix

test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

source scripts/funcs/printing
source scripts/funcs/kubernetes

# Configuration of the resources for Ops Manager
title "Creating admin secret for the new Ops Manager instance"
kubectl --namespace "${NAMESPACE}" delete secrets ops-manager-admin-secret --ignore-not-found
kubectl --namespace "${NAMESPACE}" create secret generic ops-manager-admin-secret  \
        --from-literal=Username="jane.doe@example.com" \
        --from-literal=Password="Passw0rd." \
        --from-literal=FirstName="Jane" \
        --from-literal=LastName="Doe" -n "${NAMESPACE}"

# Configuration of the resources for MongoDB
title "Creating project and credentials Kubernetes object..."

if [ -n "${OM_BASE_URL-}" ]; then
  BASE_URL="${OM_BASE_URL}"
else
  BASE_URL="http://ops-manager-svc.${OPS_MANAGER_NAMESPACE:-}.svc.cluster.local:8080"
fi

# We always create the image pull secret from the docker config.json which gives access to all necessary image repositories
create_image_registries_secret

# delete `my-project` if it exists
kubectl --namespace "${NAMESPACE}" delete configmap my-project --ignore-not-found
# Configuring project
kubectl --namespace "${NAMESPACE}" create configmap my-project \
        --from-literal=projectName="${NAMESPACE}" --from-literal=baseUrl="${BASE_URL}" \
        --from-literal=orgId="${OM_ORGID:-}"

if [[ -z ${OM_USER-} ]] || [[ -z ${OM_API_KEY-} ]]; then
    echo "OM_USER and/or OM_API_KEY env variables are not provided - assuming this is an"
    echo "e2e test for MongoDbOpsManager, skipping creation of the credentials secret"
else
    # delete `my-credentials` if it exists
    kubectl --namespace "${NAMESPACE}" delete  secret my-credentials  --ignore-not-found
    # Configure the Kubernetes credentials for Ops Manager
    if [[ -z ${OM_PUBLIC_API_KEY:-} ]]; then
         kubectl --namespace "${NAMESPACE}" create secret generic my-credentials \
            --from-literal=user="${OM_USER:-admin}" --from-literal=publicApiKey="${OM_API_KEY}"
    else
        kubectl --namespace "${NAMESPACE}" create secret generic my-credentials \
            --from-literal=user="${OM_PUBLIC_API_KEY}" --from-literal=publicApiKey="${OM_API_KEY}"
    fi
fi

title "All necessary ConfigMaps and Secrets have been created"
