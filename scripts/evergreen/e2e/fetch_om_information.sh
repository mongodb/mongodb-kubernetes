#!/usr/bin/env bash
set -Eeou pipefail -o posix

source scripts/funcs/checks
source scripts/funcs/printing

[[ "${MODE-}" = "dev" ]] && return

if [[ "${TEST_MODE:-}" = "opsmanager" ]]; then
    echo "Skipping Ops Manager connection configuration as current test is for Ops Manager"
    return
fi

if [[ "${OM_EXTERNALLY_CONFIGURED:-}" = "true" ]]; then
    echo "Skipping Ops Manager connection configuration as the connection details are already provided"
    return
fi

title "Reading Ops Manager environment variables..."

check_env_var "OPS_MANAGER_NAMESPACE" "The 'OPS_MANAGER_NAMESPACE' must be specified to fetch Ops Manager connection details"

OPERATOR_TESTING_FRAMEWORK_NS=${OPS_MANAGER_NAMESPACE}
if ! kubectl get "namespace/${OPERATOR_TESTING_FRAMEWORK_NS}" &> /dev/null; then
    error "Ops Manager is not installed in this cluster. Make sure the Ops Manager installation script is called beforehand. Exiting..."

    exit 1
else
    echo "Ops Manager is already installed in this cluster. Will reuse it now."
fi

echo "Getting credentials from secrets"


OM_USER="$(kubectl get secret ops-manager-admin-secret -n "${OPERATOR_TESTING_FRAMEWORK_NS}"  -o json | jq -r '.data | with_entries(.value |= @base64d)' | jq '.Username' -r)"
OM_PASSWORD="$(kubectl get secret ops-manager-admin-secret  -n "${OPERATOR_TESTING_FRAMEWORK_NS}" -o json | jq -r '.data | with_entries(.value |= @base64d)' | jq '.Password' -r)"

OM_PUBLIC_API_KEY="$(kubectl get secret "${OPERATOR_TESTING_FRAMEWORK_NS}"-ops-manager-admin-key -n "${OPERATOR_TESTING_FRAMEWORK_NS}" -o json | jq -r '.data | with_entries(.value |= @base64d)' | jq '.publicKey' -r)"
OM_API_KEY="$(kubectl get secret "${OPERATOR_TESTING_FRAMEWORK_NS}"-ops-manager-admin-key -n "${OPERATOR_TESTING_FRAMEWORK_NS}" -o json | jq -r '.data | with_entries(.value |= @base64d)' | jq '.privateKey' -r)"

export OM_USER
export OM_PASSWORD
export OM_API_KEY
export OM_PUBLIC_API_KEY


title "Ops Manager environment is successfully read"
