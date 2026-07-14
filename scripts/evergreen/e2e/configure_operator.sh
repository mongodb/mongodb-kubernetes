#!/usr/bin/env bash
set -Eeou pipefail

test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

source scripts/funcs/printing
source scripts/funcs/kubernetes

# Configuration of the resources for Ops Manager
title "Creating admin secret for the new Ops Manager instance"
kubectl apply -f - <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: ops-manager-admin-secret
  namespace: ${NAMESPACE}
type: Opaque
stringData:
  Username: "jane.doe@example.com"
  Password: "Passw0rd."
  FirstName: "Jane"
  LastName: "Doe"
EOF

# Configuration of the resources for MongoDB
title "Creating project and credentials Kubernetes object..."

if [ -n "${OM_BASE_URL-}" ]; then
  BASE_URL="${OM_BASE_URL}"
else
  BASE_URL="http://ops-manager-svc.${OPS_MANAGER_NAMESPACE:-}.svc.cluster.local:8080"
fi

create_image_registries_secret

# In local multi-cluster runs, prepare_multi_cluster.go creates my-project and my-credentials
# via the Go API immediately after this script. Skip the redundant kubectl calls (~0.7s).
# In single-cluster or CI, this is the only place these resources are created.
if [[ "${KUBE_ENVIRONMENT_NAME:-}" != "multi" ]] || [[ "${RUNNING_IN_EVG:-false}" == "true" ]]; then
  # Configuring project (using apply for idempotency)
  kubectl apply -f - <<EOF
apiVersion: v1
kind: ConfigMap
metadata:
  name: my-project
  namespace: ${NAMESPACE}
data:
  projectName: "${NAMESPACE}"
  baseUrl: "${BASE_URL}"
  orgId: "${OM_ORGID:-}"
EOF

  if [[ -z ${OM_USER-} ]] || [[ -z ${OM_API_KEY-} ]]; then
      echo "OM_USER and/or OM_API_KEY env variables are not provided - assuming this is an"
      echo "e2e test for MongoDbOpsManager, skipping creation of the credentials secret"
  else
      # Configure the Kubernetes credentials for Ops Manager (using apply for idempotency)
      cred_user="${OM_USER:-admin}"
      if [[ -n ${OM_PUBLIC_API_KEY:-} ]]; then
          cred_user="${OM_PUBLIC_API_KEY}"
      fi
      kubectl apply -f - <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: my-credentials
  namespace: ${NAMESPACE}
type: Opaque
stringData:
  user: "${cred_user}"
  publicApiKey: "${OM_API_KEY}"
EOF
  fi
fi

title "All necessary ConfigMaps and Secrets have been created"
