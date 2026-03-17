#!/usr/bin/env bash
# Show all running pods in the namespace
#
# This gives an overview of the deployment including:
# - MongoDB sharded cluster pods (simulated external)
# - mongot pods (MongoDB Search)
# - Envoy proxy pods (operator-managed)
# - Operator pods

echo "Pods in namespace '${MDB_NS}':"
echo ""

kubectl get pods -n "${MDB_NS}" --context "${K8S_CTX}" -o wide

echo ""
echo "Services in namespace '${MDB_NS}':"
kubectl get services -n "${MDB_NS}" --context "${K8S_CTX}" | grep -E "NAME|search|proxy|mongot"

echo ""
echo "MongoDBSearch resources:"
kubectl get mongodbsearch -n "${MDB_NS}" --context "${K8S_CTX}"

