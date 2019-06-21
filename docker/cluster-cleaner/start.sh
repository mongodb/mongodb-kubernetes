#!/usr/bin/env sh

if [ -n "${DELETE_OPS_MANAGER}" ]; then
    echo "Restarting the Ops Manager Pod."
    if [ -z "${OM_NAMESPACE}" ]; then
        echo "OM_NAMESPACE env variable is not specified";
        exit 1
    fi
    # Never delete the namespace as it is there where
    # this script should run. Instead remove resources from inside it.
    kubectl --namespace ${OM_NAMESPACE} delete sts/mongodb-enterprise-ops-manager
    kubectl --namespace ${OM_NAMESPACE} delete pvc --all
    kubectl --namespace ${OM_NAMESPACE} delete pv --all
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

        csrs_in_namespace="$(kubectl get csr -o name | grep ${namespace_name})"
        kubectl delete ${csrs_in_namespace}

        kubectl delete mdb --all -n "${namespace_name=}"
        kubectl delete mdbu --all -n "${namespace_name=}"
        kubectl delete "${namespace}"
    done
fi
