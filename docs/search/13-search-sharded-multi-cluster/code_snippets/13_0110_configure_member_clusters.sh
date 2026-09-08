echo "Configuring member clusters (credentials + RBAC + MemberCluster registration)..."

# Apply the member-cluster credentials (ServiceAccount and its long-lived token
# Secret) to every member cluster, including the central one. Users provision these
# themselves; the CLI's RBAC rendering and cluster registration both reference the
# ServiceAccount name passed via --member-cluster-service-account, and the token Secret
# is discovered via the kubernetes.io/service-account.name annotation.
for ctx in "${K8S_CTX_0}" "${K8S_CTX_1}"; do
  kubectl apply --context "${ctx}" -f - <<EOF
apiVersion: v1
kind: ServiceAccount
metadata:
  name: mck-member-sa
  namespace: "${MDB_NS}"
---
apiVersion: v1
kind: Secret
metadata:
  name: mck-member-token
  namespace: "${MDB_NS}"
  annotations:
    kubernetes.io/service-account.name: mck-member-sa
type: kubernetes.io/service-account-token
EOF
done

# Render and apply member-cluster RBAC (Roles and bindings) to every member cluster,
# including the central one.
for ctx in "${K8S_CTX_0}" "${K8S_CTX_1}"; do
  kubectl mongodb multicluster generate-member-resources \
    --member-cluster-namespace="${MDB_NS}" \
    --member-cluster-service-account=mck-member-sa \
    ${IMAGE_PULL_SECRET_NAME:+--image-pull-secrets="${IMAGE_PULL_SECRET_NAME}"} \
    | kubectl apply --context "${ctx}" -f -
done

# Register each member cluster with the operator.
for ctx in "${K8S_CTX_0}" "${K8S_CTX_1}"; do
  kubectl mongodb multicluster generate-member-registration \
    --member-cluster="${ctx}" \
    --member-cluster-context="${ctx}" \
    --member-cluster-namespace="${MDB_NS}" \
    --member-cluster-service-account=mck-member-sa \
    --operator-namespace="${MDB_NS}" \
    | kubectl apply --context "${K8S_CTX_0}" -f -
done

echo "[ok] Member clusters configured"
