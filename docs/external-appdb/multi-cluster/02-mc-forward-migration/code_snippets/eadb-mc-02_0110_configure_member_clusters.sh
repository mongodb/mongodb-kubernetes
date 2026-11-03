# Day-2 multi-cluster configuration, after the operator is installed. First
# apply member-cluster RBAC to every cluster, then register each member
# cluster with the operator on the central cluster — the helm install has
# created the MemberCluster CRD by now.
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

echo "Registering member clusters with the operator..."

# The logical name (--member-cluster-logical-name) is what
# clusterSpecList[].clusterName refers to; kind contexts contain hyphens and
# are already RFC 1123 compliant, but pass it explicitly so the MemberCluster
# CR's spec.clusterName matches the context name.
for ctx in "${K8S_CTX_0}" "${K8S_CTX_1}" "${K8S_CTX_2}"; do
  kubectl mongodb multicluster generate-member-registration \
    --member-cluster="${ctx}" \
    --member-cluster-context="${ctx}" \
    --member-cluster-namespace="${MDB_NS}" \
    --operator-namespace="${MDB_NS}" \
    --member-cluster-logical-name="${ctx}" \
    | kubectl apply --context "${K8S_CTX_0}" -f -
done

echo "[ok] Member clusters registered"
