# Render and apply member-cluster RBAC (ServiceAccount, token Secret, Roles and
# bindings) to every member cluster. The rendered names are unified (mck-member-*),
# so the output is identical for every cluster.
for ctx in "${K8S_CLUSTER_0_CONTEXT_NAME}" "${K8S_CLUSTER_1_CONTEXT_NAME}" "${K8S_CLUSTER_2_CONTEXT_NAME}"; do
  echo "Applying member-cluster RBAC on ${ctx}"
  kubectl mongodb multicluster generate-member-resources \
    --member-cluster-namespace="${OM_NAMESPACE}" \
    --workload-namespaces="${OM_NAMESPACE},${MDB_NAMESPACE}" \
    --image-pull-secrets=image-registries-secret \
    | kubectl apply --context "${ctx}" -f -
done

# Register each member cluster with the operator.
for ctx in "${K8S_CLUSTER_0_CONTEXT_NAME}" "${K8S_CLUSTER_1_CONTEXT_NAME}" "${K8S_CLUSTER_2_CONTEXT_NAME}"; do
  member_cluster_name="${ctx//_/-}"
  echo "Registering ${ctx} with the operator on the central cluster"
  kubectl mongodb multicluster generate-member-registration \
    --member-cluster="${member_cluster_name}" \
    --member-cluster-context="${ctx}" \
    --member-cluster-namespace="${OM_NAMESPACE}" \
    --operator-namespace="${OPERATOR_NAMESPACE}" \
    --member-cluster-logical-name="${ctx}" \
    | kubectl apply --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -f -
done

echo "Member clusters configured"
