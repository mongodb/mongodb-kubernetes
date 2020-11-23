#!/usr/bin/env bash

#
# Cleans Openshift from residuals from old tests
# Go to: https://console-openshift-console.apps.openshift.mongokubernetes.com/
# Click on Copy Login Command
# Run `oc` command as instructed
# Run this script
#
set -Eeou pipefail
set -o xtrace


three_hours_ago=$(date -v-3H +"%s")
namespaces=$(kubectl get ns | grep "^a-" | awk '{print $1}')

for namespace in ${namespaces};
do
    echo "Processing ${namespace}"

    namespace_dt=$(echo "${namespace}" | cut -d"-" -f 2)

    if ((namespace_dt > three_hours_ago)); then
        echo "Skipping"
        continue
    fi

    service=$(kubectl -n "$namespace" get services -o name)
    if [[ $service != "" ]]; then
        echo " > Removing finalizer from service: ${service}"
        kubectl -n "$namespace" patch "$service" --type=json -p '[{"op": "remove", "path": "/metadata/finalizers"}]'
    fi

    operator_pod=$(kubectl -n "$namespace" get pods -l app=mongodb-enterprise-operator -o name)
    if [[ $operator_pod != "" ]]; then
        echo " > Force removal of pod: $operator_pod"
        kubectl -n "$namespace" delete "$operator_pod" --grace-period=0 --force --wait=false
    fi

    # We have to remove the Pods with this annotation. After the annotation has been cleared-up
    # we need to force-remove the pods.
    remaining_pods=$(kubectl -n "${namespace}" get pods -o name)
    for pod in ${remaining_pods}; do
        kubectl -n "${namespace}" annotate "${pod}" k8s.v1.cni.cncf.io/networks-status-
        kubectl -n "${namespace}" delete "${pod}" --grace-period=0 --force --wait=false
    done

    echo " > Removing namespace: ${namespace}"
    kubectl delete "ns/${namespace}" --wait=false
    sleep 5
done
