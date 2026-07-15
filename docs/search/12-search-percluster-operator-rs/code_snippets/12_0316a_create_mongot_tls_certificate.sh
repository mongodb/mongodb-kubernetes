echo "Creating the mongot TLS certificate..."
echo "The secret NAME is cluster-invariant (\${prefix}-\${name}-search-cert), but its"
echo "CONTENT must cover every cluster's local mongot Service AND proxy Service --"
echo "the same Secret is applied verbatim to every member cluster below."

cert_name="${SEARCH_RESOURCE_NAME}-search-0"
secret_name="${SEARCH_TLS_CERT_SECRET_PREFIX}-${SEARCH_RESOURCE_NAME}-search-cert"

kubectl apply --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${cert_name}
spec:
  secretName: ${secret_name}
  duration: 8760h    # 1 year
  renewBefore: 720h  # 30 days
  privateKey:
    algorithm: RSA
    size: 2048
  usages:
    - server auth
    - client auth
  dnsNames:
    - "${SEARCH_RESOURCE_NAME}-search-${SEARCH_CLUSTER_0_INDEX}-svc.${MDB_NAMESPACE}.svc.cluster.local"
    - "${SEARCH_RESOURCE_NAME}-search-${SEARCH_CLUSTER_0_INDEX}-proxy-svc.${MDB_NAMESPACE}.svc.cluster.local"
    - "${SEARCH_RESOURCE_NAME}-search-${SEARCH_CLUSTER_1_INDEX}-svc.${MDB_NAMESPACE}.svc.cluster.local"
    - "${SEARCH_RESOURCE_NAME}-search-${SEARCH_CLUSTER_1_INDEX}-proxy-svc.${MDB_NAMESPACE}.svc.cluster.local"
    - "${SEARCH_RESOURCE_NAME}-search-${SEARCH_CLUSTER_2_INDEX}-svc.${MDB_NAMESPACE}.svc.cluster.local"
    - "${SEARCH_RESOURCE_NAME}-search-${SEARCH_CLUSTER_2_INDEX}-proxy-svc.${MDB_NAMESPACE}.svc.cluster.local"
  issuerRef:
    name: ${MDB_TLS_CA_ISSUER}
    kind: ClusterIssuer
EOF

kubectl wait --for=condition=Ready certificate/"${cert_name}" \
  -n "${MDB_NAMESPACE}" \
  --context "${K8S_CLUSTER_0_CONTEXT_NAME}" \
  --timeout=60s

echo "[ok] mongot TLS certificate issued on ${K8S_CLUSTER_0_CONTEXT_NAME}"

for ctx in "${K8S_CLUSTER_1_CONTEXT_NAME}" "${K8S_CLUSTER_2_CONTEXT_NAME}"; do
  kubectl get secret "${secret_name}" -n "${MDB_NAMESPACE}" --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -o json \
    | jq 'del(.metadata.uid, .metadata.resourceVersion, .metadata.creationTimestamp, .metadata.selfLink, .metadata.managedFields, .metadata.ownerReferences, .status)' \
    | kubectl apply --context "${ctx}" -n "${MDB_NAMESPACE}" -f -

  echo "  [ok] mongot TLS secret '${secret_name}' replicated to ${ctx}"
done
