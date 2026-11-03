# Multi-cluster install. First wire up member-cluster RBAC and registrations with the
# kubectl-mongodb plugin, then install the operator on the central cluster.
echo "Configuring member clusters (RBAC + MemberCluster registration)..."

# Render and apply member-cluster RBAC (ServiceAccount, token Secret, Roles and
# bindings) to every member cluster, including the central one. The rendered names
# are unified (mck-member-*), so the output is identical for every cluster.
for ctx in "${K8S_CTX_0}" "${K8S_CTX_1}" "${K8S_CTX_2}"; do
  kubectl mongodb multicluster generate-member-resources \
    --member-cluster-namespace="${MDB_NS}" \
    ${IMAGE_PULL_SECRET_NAME:+--image-pull-secrets="${IMAGE_PULL_SECRET_NAME}"} \
    | kubectl apply --context "${ctx}" -f -
done

# Register each member cluster with the operator on the central cluster. The logical
# name (--member-cluster-logical-name) is what clusterSpecList[].clusterName refers to;
# kind contexts contain hyphens and are already RFC 1123 compliant, but pass it
# explicitly so the MemberCluster CR's spec.clusterName matches the context name.
for ctx in "${K8S_CTX_0}" "${K8S_CTX_1}" "${K8S_CTX_2}"; do
  kubectl mongodb multicluster generate-member-registration \
    --member-cluster="${ctx}" \
    --member-cluster-context="${ctx}" \
    --member-cluster-namespace="${MDB_NS}" \
    --operator-namespace="${MDB_NS}" \
    --member-cluster-logical-name="${ctx}" \
    | kubectl apply --context "${K8S_CTX_0}" -f -
done

echo "[ok] Member clusters configured"

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
