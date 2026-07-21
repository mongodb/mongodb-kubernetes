echo "Installing a distinct MongoDB Kubernetes Operator release into every member cluster..."
echo "Each release watches MongoDBSearch only, scoped to its own cluster identity."

for ctx in "${K8S_CLUSTER_0_CONTEXT_NAME}" "${K8S_CLUSTER_1_CONTEXT_NAME}" "${K8S_CLUSTER_2_CONTEXT_NAME}"; do
  echo "Installing ${SEARCH_OPERATOR_RELEASE_NAME} into ${ctx} (clusterIdentity.clusterName=${ctx})..."

  # createResourcesServiceAccountsAndRoles=false: `kubectl mongodb multicluster setup`
  # (ra-02) already created the MongoDB/AppDB/database ServiceAccounts and Roles in this
  # namespace on every member cluster. Re-rendering them from a second Helm release fails
  # Helm 3's ownership-metadata check. operator.createOperatorServiceAccount stays true --
  # the operator's OWN ServiceAccount is named after operator.name, which is unique to
  # this release.
  helm upgrade --install "${SEARCH_OPERATOR_RELEASE_NAME}" "${OPERATOR_HELM_CHART}" \
    --kube-context "${ctx}" \
    --namespace "${MDB_NAMESPACE}" \
    --set operator.name="${SEARCH_OPERATOR_NAME}" \
    --set operator.clusterIdentity.clusterName="${ctx}" \
    --set 'operator.watchedResources={mongodbsearch}' \
    --set operator.createOperatorServiceAccount=true \
    --set operator.createResourcesServiceAccountsAndRoles=false \
    ${OPERATOR_ADDITIONAL_HELM_VALUES:+--set ${OPERATOR_ADDITIONAL_HELM_VALUES}} \
    --wait \
    --timeout 5m

  kubectl rollout status "deployment/${SEARCH_OPERATOR_NAME}" \
    --namespace "${MDB_NAMESPACE}" \
    --context "${ctx}" \
    --timeout=120s

  echo "  [ok] ${SEARCH_OPERATOR_NAME} ready in ${ctx}"
done
