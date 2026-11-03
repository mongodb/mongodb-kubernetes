# Multi-cluster install. First apply member-cluster RBAC with the kubectl-mongodb
# plugin (plain YAML, no CRDs required), then install the operator on the central
# cluster. MemberCluster registration happens after this script, once the CRDs exist.
echo "Applying member-cluster RBAC..."

# Render and apply member-cluster RBAC (ServiceAccount, token Secret, Roles and
# bindings) to every member cluster, including the central one. The rendered names
# are unified (mck-member-*), so the output is identical for every cluster.
for ctx in "${K8S_CTX_0}" "${K8S_CTX_1}" "${K8S_CTX_2}"; do
  kubectl mongodb multicluster generate-member-resources \
    --member-cluster-namespace="${MDB_NS}" \
    ${IMAGE_PULL_SECRET_NAME:+--image-pull-secrets="${IMAGE_PULL_SECRET_NAME}"} \
    | kubectl apply --context "${ctx}" -f -
done

echo "[ok] Member-cluster RBAC applied"

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
