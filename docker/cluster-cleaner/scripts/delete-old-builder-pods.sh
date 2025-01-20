#!/usr/bin/env bash

NAMESPACE=construction-site

if [ -z ${DELETE_OLDER_THAN_AMOUNT+x} ] || [ -z ${DELETE_OLDER_THAN_UNIT+x} ]; then
    echo "Need to set both 'DELETE_OLDER_THAN_AMOUNT' and 'DELETE_OLDER_THAN_UNIT' environment variables."
    exit 1
fi

for pod in $(kubectl -n ${NAMESPACE} get pods -o name); do
    creation_time=$(kubectl -n ${NAMESPACE} get "${pod}" -o jsonpath='{.metadata.creationTimestamp}')
    status=$(kubectl get "${pod}" -o jsonpath='{.status.phase}' -n "${NAMESPACE}")

    if [[ "${status}" != "Succeeded" ]] && [[ "${status}" != "Failed" ]]; then
        # we don't remove pending tasks
        continue
    fi

    if ! ./is_older_than.py "${creation_time}" "${DELETE_OLDER_THAN_AMOUNT}" "${DELETE_OLDER_THAN_UNIT}"; then
        continue
    fi
    kubectl -n ${NAMESPACE} delete "${pod}"
done
