if [[ -z "${IMAGE_PULL_SECRET_NAME:-}" ]]; then
  echo "No IMAGE_PULL_SECRET_NAME configured, skipping image pull secret creation"
elif [[ -z "${IMAGE_PULL_SECRET_DATA:-}" ]]; then
  echo "No IMAGE_PULL_SECRET_DATA configured, skipping image pull secret creation"
else
  for ctx in "${K8S_CLUSTER_0_CONTEXT_NAME}" "${K8S_CLUSTER_1_CONTEXT_NAME}" "${K8S_CLUSTER_2_CONTEXT_NAME}"; do
    kubectl create secret docker-registry "${IMAGE_PULL_SECRET_NAME}" \
      --docker-server="${DOCKER_REGISTRY:-quay.io}" \
      --docker-username="${DOCKER_USERNAME:-}" \
      --docker-password="${DOCKER_PASSWORD:-}" \
      --namespace="${MDB_NAMESPACE}" \
      --context "${ctx}" \
      --dry-run=client -o yaml | kubectl apply --context "${ctx}" -f -

    echo "  [ok] Image pull secret '${IMAGE_PULL_SECRET_NAME}' created in ${ctx}"
  done
fi
