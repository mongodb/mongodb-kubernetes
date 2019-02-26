#!/usr/bin/env sh

echo "This is the cluster cleaner."

tasks="e2e_standalone_config_map
e2e_standalone_schema_validation
e2e_standalone_recovery
e2e_standalone_recovery_k8s
e2e_replica_set
e2e_replica_set_config_map
e2e_replica_set_ent
e2e_replica_set_recovery
e2e_replica_set_pv
e2e_replica_set_pv_multiple
e2e_replica_set_different_namespaces
e2e_sharded_cluster
e2e_sharded_cluster_pv
e2e_sharded_cluster_recovery
e2e_sharded_cluster_secret"


if [ -n "${DELETE_OPS_MANAGER}" ]; then
    # Never delete the namespace "operator-testing" as it is there where
    # this script should run. Instead remove resources from inside it.
    
    kubectl --namespace operator-testing delete pod/mongodb-enterprise-ops-manager-0
else
    for task in ${tasks}; do
        echo "Deleting namespaces with task: ${task}"
        for namespace in $(kubectl get namespace -l "evergreen.mongodb.com/task-name=${task}"); do
            creation_time=$(kubectl delete "namespace/${namespace}" -o jsonpath='{.metadata.creationTimestamp}')

            if [ -n "${DELETE_OLDER_THAN_UNIT}" ]; then
                # only delete namespaces older than 12 hours
                if ! ./is_older_than.py "${creation_time}" "${DELETE_OLDER_THAN_AMOUNT}" "${DELETE_OLDER_THAN_UNITS}"; then
                    continue
                fi
            fi
            kubectl delete mrs --all -n "${namespace}"
            kubectl delete msc --all -n "${namespace}"
            kubectl delete mst --all -n "${namespace}"

            kubectl delete "namespace/${namespace}"
        done
    done
fi
