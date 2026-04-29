echo "Pods in ${MEMBER_CLUSTER_0_NAME} (${K8S_CLUSTER_0_CTX}):"
kubectl get pods -n "${MDB_NS}" --context "${K8S_CLUSTER_0_CTX}" -o wide

echo ""
echo "Pods in ${MEMBER_CLUSTER_1_NAME} (${K8S_CLUSTER_1_CTX}):"
kubectl get pods -n "${MDB_NS}" --context "${K8S_CLUSTER_1_CTX}" -o wide

echo ""
echo "MongoDBSearch resource (central):"
kubectl get mongodbsearch -n "${MDB_NS}" --context "${K8S_CENTRAL_CTX}"
