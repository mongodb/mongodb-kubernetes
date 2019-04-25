#!/usr/bin/env sh

if [ -n "${DELETE_OPS_MANAGER}" ]; then
    echo "Restarting the Ops Manager Pod."
    # Never delete the namespace "operator-testing" as it is there where
    # this script should run. Instead remove resources from inside it.
    kubectl --namespace operator-testing delete pod/mongodb-enterprise-ops-manager-0
else

    if [ -z ${DELETE_OLDER_THAN_AMOUNT+x} ] || [ -z ${DELETE_OLDER_THAN_UNIT+x} ]; then
        echo "Need to set both 'DELETE_OLDER_THAN_AMOUNT' and 'DELETE_OLDER_THAN_UNIT' environment variables."
        exit 1
    fi

    echo "Deleting evg tasks that are older than ${DELETE_OLDER_THAN_AMOUNT} ${DELETE_OLDER_THAN_UNIT}"
    for namespace in $(kubectl get namespace -l "evg=task" -o name); do
        creation_time=$(kubectl get "${namespace}" -o jsonpath='{.metadata.creationTimestamp}')

        if ! ./is_older_than.py "${creation_time}" "${DELETE_OLDER_THAN_AMOUNT}" "${DELETE_OLDER_THAN_UNIT}"; then
            continue
        fi

        namespace_name=$(echo "${namespace}" | cut -d '/' -f 2)
        kubectl delete mdb --all -n "${namespace_name=}"
        kubectl delete "${namespace}"
    done
fi
