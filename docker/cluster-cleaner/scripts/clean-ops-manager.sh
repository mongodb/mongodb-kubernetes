#!/usr/bin/env sh

if [ -z "${OM_NAMESPACE}" ]; then
    echo "OM_NAMESPACE env variable is not specified";
    exit 1
fi

echo "Removing Ops Manager in ${OM_NAMESPACE}"

kubectl --namespace "${OM_NAMESPACE}" delete om ops-manager

