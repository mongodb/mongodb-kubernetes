# Configure member clusters: apply member RBAC on each cluster, then register each with the operator.
echo "Applying member-cluster RBAC..."

for ctx in "${K8S_CTX_0}" "${K8S_CTX_1}" "${K8S_CTX_2}"; do
  kubectl mongodb cluster generate-resources \
    --member-cluster-namespace="${MDB_NS}" \
    ${IMAGE_PULL_SECRET_NAME:+--image-pull-secrets="${IMAGE_PULL_SECRET_NAME}"} \
    | kubectl apply --context "${ctx}" -f -
done

echo "[ok] Member-cluster RBAC applied"

echo "Registering member clusters with the operator..."

for ctx in "${K8S_CTX_0}" "${K8S_CTX_1}" "${K8S_CTX_2}"; do
  kubectl mongodb cluster generate-registration \
    --member-cluster="${ctx}" \
    --member-cluster-context="${ctx}" \
    --member-cluster-namespace="${MDB_NS}" \
    --operator-namespace="${MDB_NS}" \
    | kubectl apply --context "${K8S_CTX_0}" -f -
done

echo "[ok] Member clusters registered"
