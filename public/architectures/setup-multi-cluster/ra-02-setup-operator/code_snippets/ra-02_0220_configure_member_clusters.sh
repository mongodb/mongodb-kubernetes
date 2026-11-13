# Render and apply member-cluster RBAC (ServiceAccount, token Secret, Roles and
# bindings) to every member cluster. The rendered names are unified (mck-member-*),
# so the output is identical for every cluster.
for ctx in "${K8S_CLUSTER_0_CONTEXT_NAME}" "${K8S_CLUSTER_1_CONTEXT_NAME}" "${K8S_CLUSTER_2_CONTEXT_NAME}"; do
  echo "Applying member-cluster RBAC on ${ctx}"
  kubectl mongodb cluster generate-resources \
    --member-cluster="${ctx}" \
    --operator-namespace="${OPERATOR_NAMESPACE}" \
    --workload-namespaces="${OM_NAMESPACE},${MDB_NAMESPACE}" \
    --workload-image-pull-secrets=image-registries-secret \
    | kubectl apply --context "${ctx}" -f -
done

# Register each member cluster with the operator. The logical cluster name is carried
# verbatim; the MemberCluster CR and credential Secret names are derived from it.
for ctx in "${K8S_CLUSTER_0_CONTEXT_NAME}" "${K8S_CLUSTER_1_CONTEXT_NAME}" "${K8S_CLUSTER_2_CONTEXT_NAME}"; do
  echo "Registering ${ctx} with the operator on the central cluster"
  kubectl mongodb cluster generate-registration \
    --member-cluster="${ctx}" \
    --member-cluster-context="${ctx}" \
    --operator-namespace="${OPERATOR_NAMESPACE}" \
    | kubectl apply --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -f -
done

echo "Member clusters configured"
