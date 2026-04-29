echo "Verifying per-cluster Envoy Deployment in each member cluster (one Envoy per cluster, multiplexing shards via SNI)..."

envoy_deployment="${MDB_SEARCH_RESOURCE_NAME}-search-lb-0"

echo "Member ${MEMBER_CLUSTER_0_NAME} (${K8S_CLUSTER_0_CTX}):"
kubectl wait --for=condition=Available deployment/"${envoy_deployment}" \
  -n "${MDB_NS}" --context "${K8S_CLUSTER_0_CTX}" --timeout=300s
kubectl get deployment "${envoy_deployment}" -n "${MDB_NS}" --context "${K8S_CLUSTER_0_CTX}"

echo ""
echo "Member ${MEMBER_CLUSTER_1_NAME} (${K8S_CLUSTER_1_CTX}):"
kubectl wait --for=condition=Available deployment/"${envoy_deployment}" \
  -n "${MDB_NS}" --context "${K8S_CLUSTER_1_CTX}" --timeout=300s
kubectl get deployment "${envoy_deployment}" -n "${MDB_NS}" --context "${K8S_CLUSTER_1_CTX}"

echo ""
echo "[ok] Per-cluster Envoy is Available; SNI hostnames"
echo "       <clusterName>.<shardName>.search-lb.lt.example.com:443"
echo "     should now resolve through your DNS layer to each cluster's Envoy frontend,"
echo "     with per-shard routing inside Envoy."
