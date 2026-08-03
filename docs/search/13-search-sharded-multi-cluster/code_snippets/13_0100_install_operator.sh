echo "Installing the operator..."

# TODO(m1kola): slice-7 - interim: member-cluster workload RBAC is applied additively by the configure-member-clusters step below
helm upgrade --install --debug --kube-context "${K8S_CTX_0}" \
  --create-namespace --namespace="${MDB_NS}" \
  mongodb-kubernetes-operator-multi-cluster \
  --set namespace="${MDB_NS}" \
  --set operator.namespace="${MDB_NS}" \
  --set operator.watchNamespace="${MDB_NS}" \
  --set operator.name=mongodb-kubernetes-operator-multi-cluster \
  --set operator.createResourcesServiceAccountsAndRoles=false \
  ${OPERATOR_ADDITIONAL_HELM_VALUES:+--set ${OPERATOR_ADDITIONAL_HELM_VALUES}} \
  "${OPERATOR_HELM_CHART}"

kubectl --context "${K8S_CTX_0}" -n "${MDB_NS}" rollout status \
  --timeout=2m deployment/mongodb-kubernetes-operator-multi-cluster

echo "[ok] Operator installed"
