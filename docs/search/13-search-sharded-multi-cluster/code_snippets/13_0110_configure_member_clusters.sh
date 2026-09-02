echo "Configuring member clusters (RBAC + MemberCluster registration)..."

# Render and apply member-cluster RBAC (ServiceAccount, token Secret, Roles and
# bindings) to every member cluster, including the central one.
for ctx in "${K8S_CTX_0}" "${K8S_CTX_1}"; do
  kubectl mongodb multicluster generate-member-resources \
    --member-cluster="${ctx}" \
    --member-cluster-namespace="${MDB_NS}" \
    ${IMAGE_PULL_SECRET_NAME:+--image-pull-secrets="${IMAGE_PULL_SECRET_NAME}"} \
    | kubectl apply --context "${ctx}" -f -
done

# Register each member cluster with the operator.
for ctx in "${K8S_CTX_0}" "${K8S_CTX_1}"; do
  kubectl mongodb multicluster generate-member-registration \
    --member-cluster="${ctx}" \
    --member-cluster-context="${ctx}" \
    --member-cluster-namespace="${MDB_NS}" \
    --operator-namespace="${MDB_NS}" \
    | kubectl apply --context "${K8S_CTX_0}" -f -
done

echo "[ok] Member clusters configured"
