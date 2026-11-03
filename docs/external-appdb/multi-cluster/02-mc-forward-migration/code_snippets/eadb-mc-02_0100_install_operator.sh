# Multi-cluster install. Install the operator on the central cluster only;
# member-cluster configuration (RBAC on each cluster + MemberCluster
# registration) is a follow-up step, once the helm chart has created the CRDs.
echo "Installing the operator in multi-cluster mode..."

helm upgrade --install --debug --kube-context "${K8S_CTX_0}" \
  --create-namespace --namespace="${MDB_NS}" \
  mongodb-kubernetes-operator-multi-cluster \
  --set namespace="${MDB_NS}" \
  --set operator.namespace="${MDB_NS}" \
  --set operator.watchNamespace="${MDB_NS}" \
  --set operator.name=mongodb-kubernetes-operator-multi-cluster \
  ${OPERATOR_ADDITIONAL_HELM_VALUES:+--set ${OPERATOR_ADDITIONAL_HELM_VALUES}} \
  "${OPERATOR_HELM_CHART}"

echo "Waiting for the operator deployment to become available..."
kubectl --context "${K8S_CTX_0}" -n "${MDB_NS}" rollout status \
  --timeout=300s deployment/mongodb-kubernetes-operator-multi-cluster

echo "[ok] Operator installed in multi-cluster mode in namespace '${MDB_NS}'"
