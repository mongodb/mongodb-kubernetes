echo "Pods in namespace '${MDB_NS}':"
echo ""

kubectl get pods -n "${MDB_NS}" --context "${K8S_CTX}" -o wide

echo ""
echo "Services in namespace '${MDB_NS}':"
kubectl get services -n "${MDB_NS}" --context "${K8S_CTX}" | grep -E "NAME|search|proxy|mongot|lb"

echo ""
echo "MongoDBSearch resources:"
kubectl get mongodbsearch -n "${MDB_NS}" --context "${K8S_CTX}"
