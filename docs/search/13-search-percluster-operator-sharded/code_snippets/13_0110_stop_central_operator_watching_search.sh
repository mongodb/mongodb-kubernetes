# ra-02's central hub-and-spoke operator watches mongodbsearch by default
# (operator.watchedResources in helm_chart/values.yaml) and has no cluster identity, so a
# MongoDBSearch CR with more than one spec.clusters[] entry makes it write status Invalid
# ("multi-cluster MongoDBSearch is not supported yet") on every reconcile -- fighting the
# per-cluster Search operator that owns the CR and writes Running. Narrow the central
# operator to everything BUT mongodbsearch; it keeps watching mongodb, which it still
# needs to reconcile this scenario's sharded source.
helm upgrade mongodb-kubernetes-operator-multi-cluster \
  "${OPERATOR_HELM_CHART}" \
  --kube-context "${K8S_CLUSTER_0_CONTEXT_NAME}" \
  --namespace "${OPERATOR_NAMESPACE}" \
  --reuse-values \
  --set 'operator.watchedResources={mongodb,opsmanagers,mongodbusers,mongodbcommunity,voyageais}'

kubectl rollout status deployment/mongodb-kubernetes-operator-multi-cluster \
  --namespace "${OPERATOR_NAMESPACE}" \
  --context "${K8S_CLUSTER_0_CONTEXT_NAME}" \
  --timeout=120s

echo "[ok] central operator no longer watches mongodbsearch"
