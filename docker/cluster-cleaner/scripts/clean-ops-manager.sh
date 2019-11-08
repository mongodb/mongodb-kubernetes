#!/usr/bin/env sh

if [ -z "${OM_NAMESPACE}" ]; then
    echo "OM_NAMESPACE env variable is not specified";
    exit 1
fi

echo "Removing Ops Manager in ${OM_NAMESPACE}"

# We sleep for a bit between stages to make sure that the underlying Persistent Volume
# is unbound and then deallocated by Kubernetes.
kubectl --namespace "${OM_NAMESPACE}" scale sts/mongodb-enterprise-ops-manager --replicas=0
sleep 20
kubectl --namespace "${OM_NAMESPACE}" delete pvc --all
sleep 20
kubectl --namespace "${OM_NAMESPACE}" scale sts/mongodb-enterprise-ops-manager --replicas=1
