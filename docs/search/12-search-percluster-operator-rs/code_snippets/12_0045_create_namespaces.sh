echo "Creating namespace '${MDB_NAMESPACE}' in every member cluster..."

for ctx in "${K8S_CLUSTER_0_CONTEXT_NAME}" "${K8S_CLUSTER_1_CONTEXT_NAME}" "${K8S_CLUSTER_2_CONTEXT_NAME}"; do
  kubectl create namespace "${MDB_NAMESPACE}" --context "${ctx}" --dry-run=client -o yaml | \
    kubectl apply --context "${ctx}" -f -
  echo "  [ok] Namespace '${MDB_NAMESPACE}' ready in ${ctx}"
done
