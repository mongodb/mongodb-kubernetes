kubectl apply --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" -f - <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: community-private-preview-pullsecret
data:
  .dockerconfigjson: "${PRIVATE_PREVIEW_IMAGE_PULLSECRET}"
type: kubernetes.io/dockerconfigjson
EOF

pull_secrets=$(kubectl --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" \
  get sa mongodb-kubernetes-database-pods -n "${MDB_NAMESPACE}" -o=jsonpath='{.imagePullSecrets[*]}')

if [[ "${pull_secrets}" ]]; then
  kubectl patch --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" \
    sa mongodb-kubernetes-database-pods  \
    --type=json -p='[{"op": "add", "path": "/imagePullSecrets/-", "value": {"name": "community-private-preview-pullsecret"}}]'
else
  kubectl patch --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" \
    sa mongodb-kubernetes-database-pods  \
    --type=merge -p='{"imagePullSecrets": [{"name": "community-private-preview-pullsecret"}]}'
fi
echo "ServiceAccount mongodb-kubernetes-database-pods has been patched: "

kubectl get --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" -o yaml sa mongodb-kubernetes-database-pods
