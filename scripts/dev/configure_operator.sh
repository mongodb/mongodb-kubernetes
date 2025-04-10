#!/usr/bin/env bash

set -Eeou pipefail


# TODO replace in favour of 'evergreen/e2e/configure_operator'
source scripts/dev/set_env_context.sh
source scripts/funcs/kubernetes
source scripts/funcs/printing

ensure_namespace "${NAMESPACE}"

export OM_BASE_URL=${OM_HOST}

title "Configuring config map and secret for the Operator"

if [[ -z ${OM_HOST} ]]; then
    echo "OM_HOST env variable not provided - the default project ConfigMap won't be created!"
    echo "You may need to spawn new Ops Manager - call 'make om'/'make om-evg' or add parameters to "
    echo "'~/.operator-dev/om' or '~/.operator-dev/contexts/<current_context>' manually"
    echo "(Ignore this if you are working with MongoDBOpsManager custom resource)"
else
    config_map_name="my-project"
    kubectl delete configmap ${config_map_name} -n "${NAMESPACE}" 2>/dev/null || true
    kubectl create configmap ${config_map_name} --from-literal orgId="${OM_ORGID-}" --from-literal "projectName=${NAMESPACE}" --from-literal "baseUrl=${OM_HOST}" -n "${NAMESPACE}"
fi

if [[ -z ${OM_USER} ]] || [[ -z ${OM_API_KEY} ]]; then
    echo "OM_USER and/or OM_API_KEY env variables are not provided - the default credentials Secret won't be created!"
    echo "You may need to spawn new Ops Manager - call 'make om'/'make om-evg' or add parameters to "
    echo "'~/.operator-dev/om' or '~/.operator-dev/contexts/<current_context>' manually"
    echo "(Ignore this if you are working with MongoDBOpsManager custom resource)"
else
    secret_name="my-credentials"
    kubectl delete secret ${secret_name} -n "${NAMESPACE}" 2>/dev/null || true
    kubectl create secret generic ${secret_name}  --from-literal=user="${OM_USER}" --from-literal=publicApiKey="${OM_API_KEY}" -n "${NAMESPACE}"
fi

# this is the secret for OpsManager CR
om_admin_secret="ops-manager-admin-secret"
kubectl delete secret ${om_admin_secret} -n "${NAMESPACE}" 2>/dev/null || true
kubectl create secret generic ${om_admin_secret}  --from-literal=Username="jane.doe@example.com" --from-literal=Password="Passw0rd."  --from-literal=FirstName="Jane" --from-literal=LastName="Doe" -n "${NAMESPACE}"

title "All necessary ConfigMaps and Secrets for the Operator are configured"

