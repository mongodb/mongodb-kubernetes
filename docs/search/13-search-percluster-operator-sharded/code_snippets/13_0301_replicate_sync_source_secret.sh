echo "Replicating the sync-source password secret to every search cluster..."

# spec.source.passwordSecretRef is cluster-invariant: the operator's
# secrets-presence check expects the SAME secret name in every member
# cluster, not just where the MongoDBUser CRD lives. Nothing replicates
# this automatically -- create it directly in each cluster from the
# plaintext we already have.
for ctx in "${K8S_CLUSTER_0_CONTEXT_NAME}" "${K8S_CLUSTER_1_CONTEXT_NAME}"; do
  kubectl create secret generic "${MDBS_RESOURCE_NAME}-search-sync-source-password" \
    --from-literal=password="${MDBS_SEARCH_SYNC_USER_PASSWORD}" \
    --namespace "${MDB_NAMESPACE}" \
    --context "${ctx}" \
    --dry-run=client -o yaml | kubectl apply --context "${ctx}" -f -

  echo "[ok] sync-source password secret present in cluster ${ctx}"
done
