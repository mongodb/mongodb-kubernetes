# TODO(m1kola): token-wait: Simplify this workflow. We should probably make `generate-member-registration` wait for token.

# Render and apply member-cluster RBAC (ServiceAccount, token Secret, Roles and
# bindings) to every member cluster.
for ctx in "${K8S_CLUSTER_0_CONTEXT_NAME}" "${K8S_CLUSTER_1_CONTEXT_NAME}" "${K8S_CLUSTER_2_CONTEXT_NAME}"; do
  member_cluster_name="${ctx//_/-}"
  echo "Applying member-cluster RBAC on ${ctx} (member name: ${member_cluster_name})"
  kubectl mongodb multicluster generate-member-resources \
    --member-cluster="${member_cluster_name}" \
    --member-cluster-namespace="${OM_NAMESPACE}" \
    --workload-namespaces="${OM_NAMESPACE},${MDB_NAMESPACE}" \
    --image-pull-secrets=image-registries-secret \
    | kubectl apply --context "${ctx}" -f -
done

# Register each member cluster with the operator.
for ctx in "${K8S_CLUSTER_0_CONTEXT_NAME}" "${K8S_CLUSTER_1_CONTEXT_NAME}" "${K8S_CLUSTER_2_CONTEXT_NAME}"; do
  member_cluster_name="${ctx//_/-}"
  token_secret="mck-member-${member_cluster_name}-token"

  # Wait for the ServiceAccount token Secret created in stage 1 to be populated.
  echo "Waiting for ${token_secret} Secret on ${ctx} to be populated..."
  token=""
  for _ in $(seq 1 40); do
    token=$(kubectl --context "${ctx}" -n "${OM_NAMESPACE}" get secret "${token_secret}" -o jsonpath='{.data.token}' 2>/dev/null || true)
    [[ -n "${token}" ]] && break
    sleep 3
  done
  if [[ -z "${token}" ]]; then
    echo "ERROR: ${token_secret} Secret on ${ctx} was not populated within 120s" >&2
    exit 1
  fi

  echo "Registering ${ctx} with the operator on the central cluster"
  kubectl mongodb multicluster generate-member-registration \
    --member-cluster="${member_cluster_name}" \
    --member-cluster-context="${ctx}" \
    --member-cluster-namespace="${OM_NAMESPACE}" \
    --operator-namespace="${OPERATOR_NAMESPACE}" \
    --cluster-name="${ctx}" \
    | kubectl apply --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -f -
done

echo "Member clusters configured"
