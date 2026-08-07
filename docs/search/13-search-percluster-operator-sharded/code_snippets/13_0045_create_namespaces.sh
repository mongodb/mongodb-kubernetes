: "${K8S_CLUSTER_0_CONTEXT_NAME:?not set -- source the env files first (see README, Environment section)}"
: "${K8S_CLUSTER_1_CONTEXT_NAME:?not set -- source the env files first (see README, Environment section)}"
: "${MDB_NAMESPACE:?not set -- source the env files first (see README, Environment section)}"

for ctx in "${K8S_CLUSTER_0_CONTEXT_NAME}" "${K8S_CLUSTER_1_CONTEXT_NAME}"; do
  kubectl create namespace "${MDB_NAMESPACE}" \
    --context "${ctx}" \
    --dry-run=client -o yaml \
    | kubectl apply --context "${ctx}" -f -

  echo "[ok] Namespace '${MDB_NAMESPACE}' ready in cluster ${ctx}"
done
