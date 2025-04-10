#!/usr/bin/env sh

echo "Deleting ClusterRoles"

for role in $(kubectl get clusterrole -o name | grep -E "mongodb-enterprise-operator|mdb-operator|operator-multi-cluster-tests-role-binding|operator-tests-role-binding") ; do
    creation_time=$(kubectl get "${role}" -o jsonpath='{.metadata.creationTimestamp}')

    if ! ./is_older_than.py "${creation_time}" 1 hours; then
        continue
    fi

    kubectl delete "${role}"
done

echo "Deleting ClusterRoleBinding"
for binding in $(kubectl get clusterrolebinding -o name | grep -E "mongodb-enterprise-operator|mdb-operator|operator-multi-cluster-tests-role-binding|operator-tests-role-binding"); do
    creation_time=$(kubectl get "${binding}" -o jsonpath='{.metadata.creationTimestamp}')

    if ! ./is_older_than.py "${creation_time}" 1 hours; then
        continue
    fi

    kubectl delete "${binding}"
done
