echo "Creating namespace '${MDB_NS}' in central + member clusters..."

kubectl create namespace "${MDB_NS}" --context "${K8S_CENTRAL_CTX}" --dry-run=client -o yaml | \
  kubectl apply --context "${K8S_CENTRAL_CTX}" -f -

kubectl create namespace "${MDB_NS}" --context "${K8S_CLUSTER_0_CTX}" --dry-run=client -o yaml | \
  kubectl apply --context "${K8S_CLUSTER_0_CTX}" -f -

kubectl create namespace "${MDB_NS}" --context "${K8S_CLUSTER_1_CTX}" --dry-run=client -o yaml | \
  kubectl apply --context "${K8S_CLUSTER_1_CTX}" -f -

echo "[ok] Namespace '${MDB_NS}' ready in central + ${MEMBER_CLUSTER_0_NAME} + ${MEMBER_CLUSTER_1_NAME}"
