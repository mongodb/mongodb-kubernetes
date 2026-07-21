echo "Waiting for MongoDBSearch to be ready in every cluster..."
echo "This may take several minutes while mongot syncs data..."

for ctx in "${K8S_CLUSTER_0_CONTEXT_NAME}" "${K8S_CLUSTER_1_CONTEXT_NAME}"; do
  kubectl wait --for=jsonpath='{.status.phase}'=Running \
    mongodbsearch/"${MDBS_RESOURCE_NAME}" \
    -n "${MDB_NAMESPACE}" \
    --context "${ctx}" \
    --timeout=600s

  echo "[ok] MongoDBSearch '${MDBS_RESOURCE_NAME}' is Running in cluster ${ctx}"
done
