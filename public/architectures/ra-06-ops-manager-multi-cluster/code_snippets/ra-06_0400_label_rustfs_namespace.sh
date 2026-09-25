kubectl --context "${K8S_CLUSTER_0_CONTEXT_NAME}" label namespace "${RUSTFS_NAMESPACE}" istio-injection=enabled --overwrite
