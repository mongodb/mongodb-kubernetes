#!/usr/bin/env sh

touch error.log
tail -F error.log &

delete_resources_safely() {
    resource_type="$1"
    namespace="$2"

    echo "Attempting normal deletion of $resource_type in $namespace..."
    kubectl delete "${resource_type}" --all -n "${namespace}" --wait=true --timeout=10s 2>error.log|| true

    # Check if any resources are still stuck
    # Let's not fail here and continue deletion
    resources=$(kubectl get "$resource_type" -n "${namespace}" --no-headers -o custom-columns=":metadata.name" 2>error.log || true)

    for resource in ${resources}; do
        echo "${resource_type}/${resource} is still present, force deleting..."

        kubectl patch "${resource_type}" "${resource}" -n "${namespace}" -p '{"metadata":{"finalizers":null}}' --type=merge 2>error.log || true
        kubectl delete "${resource_type}" "${resource}" -n "${namespace}" --force --grace-period=0 2>error.log || true
    done
}

if [ -z ${DELETE_OLDER_THAN_AMOUNT+x} ] || [ -z ${DELETE_OLDER_THAN_UNIT+x} ]; then
    echo "Need to set both 'DELETE_OLDER_THAN_AMOUNT' and 'DELETE_OLDER_THAN_UNIT' environment variables."
    exit 1
fi

if [ -z ${LABELS+x} ]; then
    echo "Need to set 'LABELS' environment variables."
    exit 1
fi


echo "Deleting namespaces for evg tasks that are older than ${DELETE_OLDER_THAN_AMOUNT} ${DELETE_OLDER_THAN_UNIT} with label ${LABELS}"
echo "Which are:"
kubectl get namespace -l "${LABELS}" -o name
for namespace in $(kubectl get namespace -l "${LABELS}" -o name 2>error.log); do
    creation_time=$(kubectl get "${namespace}" -o jsonpath='{.metadata.creationTimestamp}' 2>error.log || echo "")

    if [ -z "$creation_time" ]; then
        echo "Namespace ${namespace} does not exist or has no creation timestamp, skipping."
        continue
    fi

    namespace_name=$(echo "${namespace}" | cut -d '/' -f 2)

    if ! ./is_older_than.py "${creation_time}" "${DELETE_OLDER_THAN_AMOUNT}" "${DELETE_OLDER_THAN_UNIT}"; then
        echo "Skipping ${namespace_name}, not old enough."
        continue
    fi

    echo "Deleting ${namespace_name}"

    csrs_in_namespace=$(kubectl get csr -o name 2>error.log | grep "${namespace_name}" 2>/dev/null || true)
    if [ -n "${csrs_in_namespace}" ]; then
        kubectl delete "${csrs_in_namespace}" 2>error.log
    fi

    delete_resources_safely "mdb" "${namespace_name}"
    delete_resources_safely "mdbu" "${namespace_name}"
    delete_resources_safely "mdbc" "${namespace_name}"
    delete_resources_safely "mdbmc" "${namespace_name}"
    delete_resources_safely "om" "${namespace_name}"
    delete_resources_safely "clustermongodbroles" "${namespace_name}"

    echo "Attempting to delete namespace: ${namespace_name}"

    if kubectl get namespace "${namespace_name}" >/dev/null 2>&1; then
        kubectl delete namespace "${namespace_name}" --wait=true --timeout=10s || true
    else
        echo "Namespace ${namespace_name} not found, skipping deletion."
    fi

    if kubectl get namespace "${namespace_name}" >/dev/null 2>&1; then
        echo "Namespace ${namespace_name} is still stuck, removing finalizers..."
        kubectl patch namespace "${namespace_name}" -p '{"metadata":{"finalizers":null}}' --type=merge

        echo "Force deleting namespace: ${namespace_name}"
        kubectl delete namespace "${namespace_name}" --wait=true --timeout=30s
    else
        echo "Namespace ${namespace_name} deleted successfully."
    fi
done
