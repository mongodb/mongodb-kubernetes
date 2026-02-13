# Show all running pods for the external sharded MongoDB cluster and MongoDBSearch

echo "=== External MongoDB Sharded Cluster Pods ==="
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" get pods -l app="${MDB_EXTERNAL_CLUSTER_NAME}" -o wide

echo ""
echo "=== MongoDBSearch (mongot) Pods ==="
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" get pods -l search.mongodb.com/mongodbsearch="${MDB_SEARCH_RESOURCE_NAME}" -o wide

echo ""
echo "=== All Services ==="
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" get svc | grep -E "(${MDB_EXTERNAL_CLUSTER_NAME}|${MDB_SEARCH_RESOURCE_NAME})"

echo ""
echo "=== StatefulSets ==="
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" get statefulsets | grep -E "(${MDB_EXTERNAL_CLUSTER_NAME}|${MDB_SEARCH_RESOURCE_NAME})"

