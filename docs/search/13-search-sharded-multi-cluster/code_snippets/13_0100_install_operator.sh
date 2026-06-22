echo "Configuring multi-cluster service accounts and roles..."

# The kubectl-mongodb plugin creates the operator's service accounts, roles and
# the multi-cluster kubeconfig Secret in every member cluster.
# Install it from https://github.com/mongodb/mongodb-kubernetes/releases
#
# The plugin embeds your kubeconfig's API server endpoints into the operator's
# kubeconfig Secret, and the operator reaches the member clusters through
# them -- so they must be resolvable from pods. Cloud-provider kubeconfigs
# are; local kind/minikube kubeconfigs (127.0.0.1 endpoints) are not. Set
# MDB_PLUGIN_KUBECONFIG to a kubeconfig variant with pod-reachable endpoints
# in that case.
KUBECONFIG="${MDB_PLUGIN_KUBECONFIG:-${KUBECONFIG:-${HOME}/.kube/config}}" \
  kubectl mongodb multicluster setup \
  --central-cluster="${K8S_CTX_0}" \
  --member-clusters="${K8S_CTX_0},${K8S_CTX_1}" \
  --member-cluster-namespace="${MDB_NS}" \
  --central-cluster-namespace="${MDB_NS}" \
  --create-service-account-secrets \
  --install-database-roles=true \
  ${IMAGE_PULL_SECRET_NAME:+--image-pull-secrets="${IMAGE_PULL_SECRET_NAME}"}

echo "[ok] Multi-cluster service accounts and roles configured"

echo "Installing the operator in multi-cluster mode..."

helm upgrade --install --debug --kube-context "${K8S_CTX_0}" \
  --create-namespace --namespace="${MDB_NS}" \
  mongodb-kubernetes-operator-multi-cluster \
  --set namespace="${MDB_NS}" \
  --set operator.namespace="${MDB_NS}" \
  --set operator.watchNamespace="${MDB_NS}" \
  --set operator.name=mongodb-kubernetes-operator-multi-cluster \
  --set operator.createOperatorServiceAccount=false \
  --set operator.createResourcesServiceAccountsAndRoles=false \
  --set "multiCluster.clusters={${K8S_CTX_0},${K8S_CTX_1}}" \
  ${OPERATOR_ADDITIONAL_HELM_VALUES:+--set ${OPERATOR_ADDITIONAL_HELM_VALUES}} \
  "${OPERATOR_HELM_CHART}"

kubectl --context "${K8S_CTX_0}" -n "${MDB_NS}" rollout status \
  --timeout=2m deployment/mongodb-kubernetes-operator-multi-cluster

echo "[ok] Operator installed in multi-cluster mode"
