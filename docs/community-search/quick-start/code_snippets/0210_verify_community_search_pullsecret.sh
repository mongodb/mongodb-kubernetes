echo "Verifying mongodb-kubernetes-database-pods service account contains proper pull secret"
if ! kubectl get --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" -o json \
  sa mongodb-kubernetes-database-pods -o=jsonpath='{.imagePullSecrets[*]}' | \
    grep community-private-preview-pullsecret; then
  echo "ERROR: mongodb-kubernetes-database-pods service account doesn't contain necessary pullsecret"
  kubectl get --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" -o json \
    sa mongodb-kubernetes-database-pods -o=yaml
  return 1
fi
echo "SUCCESS: mongodb-kubernetes-database-pods service account contains proper pull secret"
