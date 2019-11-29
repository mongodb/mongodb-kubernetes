#!/usr/bin/env sh

if [ -z ${DELETE_OLDER_THAN_AMOUNT+x} ] || [ -z ${DELETE_OLDER_THAN_UNIT+x} ]; then
    echo "Need to set both 'DELETE_OLDER_THAN_AMOUNT' and 'DELETE_OLDER_THAN_UNIT' environment variables."
    exit 1
fi

if [ -z ${LABELS+x} ]; then
    echo "Need to set 'LABELS' environment variables."
    exit 1
fi

echo "Deleting evg tasks that are older than ${DELETE_OLDER_THAN_AMOUNT} ${DELETE_OLDER_THAN_UNIT} with label ${LABELS}"
for namespace in $(kubectl get namespace -l "${LABELS}" -o name); do
    creation_time=$(kubectl get "${namespace}" -o jsonpath='{.metadata.creationTimestamp}')

    if ! ./is_older_than.py "${creation_time}" "${DELETE_OLDER_THAN_AMOUNT}" "${DELETE_OLDER_THAN_UNIT}"; then
        continue
    fi

    namespace_name=$(echo "${namespace}" | cut -d '/' -f 2)

    csrs_in_namespace=$(kubectl get csr -o name | grep "${namespace_name}")
    kubectl delete "${csrs_in_namespace}"

    kubectl delete mdb --all -n "${namespace_name=}"
    kubectl delete mdbu --all -n "${namespace_name=}"
    kubectl delete "${namespace}"
done
