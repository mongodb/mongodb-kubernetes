echo "Installing a distinct per-cluster Search operator in every member cluster..."

# Same Helm chart, a SECOND release (SEARCH_OPERATOR_RELEASE_NAME) per cluster --
# cluster 0 already runs ra-02's central hub-and-spoke operator in this namespace.
# operator.clusterIdentity.clusterName pins each release to its own cluster's
# spec.clusters[].name; operator.watchedResources narrows it to MongoDBSearch only.
for ctx_name in \
    "${K8S_CLUSTER_0_CONTEXT_NAME}|${MDBS_CLUSTER_0_NAME}" \
    "${K8S_CLUSTER_1_CONTEXT_NAME}|${MDBS_CLUSTER_1_NAME}"; do
  ctx="${ctx_name%%|*}"
  cluster_name="${ctx_name#*|}"

  helm upgrade --install --debug --kube-context "${ctx}" \
    --create-namespace \
    --namespace="${MDB_NAMESPACE}" \
    "${SEARCH_OPERATOR_RELEASE_NAME}" \
    --set operator.clusterIdentity.clusterName="${cluster_name}" \
    --set operator.watchedResources='{mongodbsearch}' \
    ${OPERATOR_ADDITIONAL_HELM_VALUES:+--set ${OPERATOR_ADDITIONAL_HELM_VALUES}} \
    "${OPERATOR_HELM_CHART}"

  echo "[ok] '${SEARCH_OPERATOR_RELEASE_NAME}' installed in cluster ${ctx} (clusterIdentity.clusterName=${cluster_name})"
done
