# Apply the member-cluster credentials (ServiceAccount and its long-lived token
# Secret) to every member cluster. Users provision these themselves; the CLI's RBAC
# rendering and cluster registration both reference the ServiceAccount name passed via
# --member-cluster-service-account, and the token Secret is discovered via the
# kubernetes.io/service-account.name annotation.
for ctx in "${K8S_CLUSTER_0_CONTEXT_NAME}" "${K8S_CLUSTER_1_CONTEXT_NAME}" "${K8S_CLUSTER_2_CONTEXT_NAME}"; do
  echo "Applying member-cluster credentials on ${ctx}"
  kubectl apply --context "${ctx}" -f - <<EOF
apiVersion: v1
kind: ServiceAccount
metadata:
  name: mck-member-sa
  namespace: "${OM_NAMESPACE}"
---
apiVersion: v1
kind: Secret
metadata:
  name: mck-member-token
  namespace: "${OM_NAMESPACE}"
  annotations:
    kubernetes.io/service-account.name: mck-member-sa
type: kubernetes.io/service-account-token
EOF
done

# Render and apply member-cluster RBAC (Roles and bindings) to every member cluster.
# The rendered names are unified (mck-member-*), so the output is identical for every
# cluster.
for ctx in "${K8S_CLUSTER_0_CONTEXT_NAME}" "${K8S_CLUSTER_1_CONTEXT_NAME}" "${K8S_CLUSTER_2_CONTEXT_NAME}"; do
  echo "Applying member-cluster RBAC on ${ctx}"
  kubectl mongodb multicluster generate-member-resources \
    --member-cluster-namespace="${OM_NAMESPACE}" \
    --member-cluster-service-account=mck-member-sa \
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
    --member-cluster-service-account=mck-member-sa \
    --operator-namespace="${OPERATOR_NAMESPACE}" \
    --member-cluster-logical-name="${ctx}" \
    | kubectl apply --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -f -
done

echo "Member clusters configured"
