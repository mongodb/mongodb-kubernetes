#!/bin/bash

source scripts/dev/set_env_context.sh

# Set Helm release name
HELM_RELEASE="mongodb-kubernetes-operator"

echo "Deleting all resources with annotation meta.helm.sh/release-name=${HELM_RELEASE}..."

# List of resource types to check
RESOURCE_TYPES=("sa" "roles" "rolebindings" "all" "clusterroles" "clusterrolebindings" "deployments" "statefulsets" "services" "configmaps" "secrets" "jobs" "cronjobs" "daemonsets" "ingresses" "networkpolicies" "pvc")

# Loop through each resource type and delete resources with the specified annotation
for RESOURCE in "${RESOURCE_TYPES[@]}"; do
    kubectl get "${RESOURCE}" --all-namespaces -o json | jq -r --arg HELM_RELEASE "${HELM_RELEASE}" '
        .items[] | select(.metadata.annotations["meta.helm.sh/release-name"] == $HELM_RELEASE) |
        "kubectl delete " + .kind + " " + .metadata.name + " -n " + .metadata.namespace
    ' | sh
done

# Delete Cluster-wide resources separately (they don't belong to a namespace)
for RESOURCE in "clusterroles" "clusterrolebindings"; do
    kubectl get "${RESOURCE}" -o json | jq -r --arg HELM_RELEASE "${HELM_RELEASE}" '
        .items[] | select(.metadata.annotations["meta.helm.sh/release-name"] == $HELM_RELEASE) |
        "kubectl delete " + .kind + " " + .metadata.name
    ' | sh
done

echo "All resources related to ${HELM_RELEASE} have been deleted."
