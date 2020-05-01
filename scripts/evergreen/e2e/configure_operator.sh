#!/usr/bin/env bash
set -Eeou pipefail

source scripts/funcs/printing

# Configuration of the resources for Ops Manager
title "Creating admin secret for the new Ops Manager instance"
kubectl --namespace "${PROJECT_NAMESPACE}" delete secrets ops-manager-admin-secret --ignore-not-found
kubectl create secret generic ops-manager-admin-secret  \
        --from-literal=Username="jane.doe@example.com" \
        --from-literal=Password="Passw0rd." \
        --from-literal=FirstName="Jane" \
        --from-literal=LastName="Doe" -n "${PROJECT_NAMESPACE}"

# Configuration of the resources for MongoDB
title "Creating project and credentials Kubernetes object..."

if [ -n "${OM_BASE_URL-}" ]; then
  BASE_URL="${OM_BASE_URL}"
else
  BASE_URL="http://ops-manager.${OPS_MANAGER_NAMESPACE:-}.svc.cluster.local:8080"
fi

if [[ -n "${ecr_registry_needs_auth:-}" ]]; then
  # Need to configure ECR Pull Secrets!
  docker_config=$(mktemp)
  scripts/dev/configure_docker "${ecr_registry:?}" > "${docker_config}"

  echo "Creating pull secret from docker configured file"
  oc -n "${PROJECT_NAMESPACE}" create secret generic "${ecr_registry_needs_auth}" \
    --from-file=.dockerconfigjson="${docker_config}" --type=kubernetes.io/dockerconfigjson
  rm "${docker_config}"

else
  echo "ECR registry authentication not manually configured"
fi

# delete `my-project` if it exists
kubectl --namespace "${PROJECT_NAMESPACE}" delete configmap my-project --ignore-not-found
# Configuring project
kubectl --namespace "${PROJECT_NAMESPACE}" create configmap my-project \
        --from-literal=projectName="${PROJECT_NAMESPACE}" --from-literal=baseUrl="${BASE_URL}" \
        --from-literal=orgId="${OM_ORGID:-}"

if [[ -z ${OM_USER-} ]] || [[ -z ${OM_API_KEY-} ]]; then
    echo "OM_USER and/or OM_API_KEY env variables are not provided - assuming this is an"
    echo "e2e test for MongoDbOpsManager, skipping creation of the credentials secret"
else
    # delete `my-credentials` if it exists
    kubectl --namespace "${PROJECT_NAMESPACE}" delete  secret my-credentials  --ignore-not-found
    # Configure the Kubernetes credentials for Ops Manager
    kubectl --namespace "${PROJECT_NAMESPACE}" create secret generic my-credentials \
            --from-literal=user="${OM_USER:=admin}" --from-literal=publicApiKey="${OM_API_KEY}"
fi

title "All necessary ConfigMaps and Secrets have been created"
