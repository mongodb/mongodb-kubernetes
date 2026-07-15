echo "Creating TLS certificates for the managed load balancer (Envoy)..."

# One server + one client cert PER CLUSTER (not per shard) -- one Envoy
# Deployment per cluster fronts every shard's mongot group in that cluster via
# SNI. Server cert SANs cover every cluster's proxy Services so the same
# wildcard-style cert keeps working if you add clusters later.
cert_pfx="${MDBS_TLS_CERT_SECRET_PREFIX}-${MDBS_RESOURCE_NAME}"

for cluster_idx in "${MDBS_CLUSTER_0_INDEX}" "${MDBS_CLUSTER_1_INDEX}"; do
  lb_server_cert="${cert_pfx}-search-lb-${cluster_idx}-cert"
  lb_client_cert="${cert_pfx}-search-lb-${cluster_idx}-client-cert"

  echo "Creating LB server certificate: ${lb_server_cert}"
  kubectl apply --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${lb_server_cert}
spec:
  secretName: ${lb_server_cert}
  duration: 8760h    # 1 year
  renewBefore: 720h  # 30 days
  privateKey:
    algorithm: RSA
    size: 2048
  usages:
    - server auth
  dnsNames:
    - "*.${MDB_NAMESPACE}.svc.cluster.local"
  issuerRef:
    name: ${MDB_TLS_CA_ISSUER}
    kind: ClusterIssuer
EOF

  echo "Creating LB client certificate: ${lb_client_cert}"
  kubectl apply --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${lb_client_cert}
spec:
  secretName: ${lb_client_cert}
  duration: 8760h    # 1 year
  renewBefore: 720h  # 30 days
  privateKey:
    algorithm: RSA
    size: 2048
  usages:
    - client auth
  dnsNames:
    - "*.${MDB_NAMESPACE}.svc.cluster.local"
  issuerRef:
    name: ${MDB_TLS_CA_ISSUER}
    kind: ClusterIssuer
EOF

  kubectl wait --for=condition=Ready certificate/"${lb_server_cert}" \
    -n "${MDB_NAMESPACE}" --context "${K8S_CLUSTER_0_CONTEXT_NAME}" --timeout=60s
  kubectl wait --for=condition=Ready certificate/"${lb_client_cert}" \
    -n "${MDB_NAMESPACE}" --context "${K8S_CLUSTER_0_CONTEXT_NAME}" --timeout=60s
done

echo "[ok] All managed load balancer (Envoy) TLS certificates created"
