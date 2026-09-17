: "${K8S_CLUSTER_0_CONTEXT_NAME:?not set -- source the env files first (see README, Environment section)}"
: "${K8S_CLUSTER_1_CONTEXT_NAME:?not set -- source the env files first (see README, Environment section)}"
: "${K8S_CLUSTER_2_CONTEXT_NAME:?not set -- source the env files first (see README, Environment section)}"
: "${MDB_NAMESPACE:?not set -- source the env files first (see README, Environment section)}"
: "${SEARCH_RESOURCE_NAME:?not set -- source the env files first (see README, Environment section)}"

echo "Waiting for MongoDBSearch to reach Running phase in every member cluster..."
echo "Each cluster's operator reconciles independently -- there is no combined status."

for ctx in "${K8S_CLUSTER_0_CONTEXT_NAME}" "${K8S_CLUSTER_1_CONTEXT_NAME}" "${K8S_CLUSTER_2_CONTEXT_NAME}"; do
  kubectl wait --for=jsonpath='{.status.phase}'=Running \
    "mongodbsearch/${SEARCH_RESOURCE_NAME}" \
    -n "${MDB_NAMESPACE}" \
    --context "${ctx}" \
    --timeout=600s

  echo "  [ok] MongoDBSearch '${SEARCH_RESOURCE_NAME}' Running in ${ctx}"
done
