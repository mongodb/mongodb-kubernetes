echo "Registering member clusters with the central operator (kubectl mongodb multicluster setup)..."

kubectl mongodb multicluster setup \
  --central-cluster="${K8S_CENTRAL_CTX}" \
  --member-clusters="${K8S_CLUSTER_0_CTX},${K8S_CLUSTER_1_CTX}" \
  --member-cluster-namespace="${MDB_NS}" \
  --central-cluster-namespace="${OPERATOR_NAMESPACE}" \
  --create-service-account-secrets \
  --install-database-roles=true

echo "[ok] kubectl-mongodb multicluster setup complete"
