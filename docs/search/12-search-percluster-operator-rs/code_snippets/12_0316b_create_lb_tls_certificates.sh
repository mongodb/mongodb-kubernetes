echo "Creating per-cluster managed load balancer (Envoy) TLS certificates..."
echo "Like the mongot cert, these are issued ONCE from cert-manager on"
echo "${K8S_CLUSTER_0_CONTEXT_NAME} (matches test_deploy_lb_certificates in"
echo "tests/multicluster_search/simulated_mc_rs.py, which never switches API client per"
echo "cluster). Each cluster gets its OWN cert name/secret, and every server cert's SANs"
echo "cover ALL clusters' proxy-svc FQDNs -- but only the resulting Secret is copied out,"
echo "to just the OWNING cluster, since each cluster's Envoy only presents its own cert."

# Union of every cluster's proxy-svc FQDN -- goes on EVERY cluster's LB server cert.
proxy_svc_0="${SEARCH_RESOURCE_NAME}-search-${SEARCH_CLUSTER_0_INDEX}-proxy-svc.${MDB_NAMESPACE}.svc.cluster.local"
proxy_svc_1="${SEARCH_RESOURCE_NAME}-search-${SEARCH_CLUSTER_1_INDEX}-proxy-svc.${MDB_NAMESPACE}.svc.cluster.local"
proxy_svc_2="${SEARCH_RESOURCE_NAME}-search-${SEARCH_CLUSTER_2_INDEX}-proxy-svc.${MDB_NAMESPACE}.svc.cluster.local"

issue_lb_certs_for_index() {
  local idx=$1

  local deployment_name="${SEARCH_RESOURCE_NAME}-search-lb-${idx}"
  local server_cert_name="${SEARCH_TLS_CERT_SECRET_PREFIX}-${SEARCH_RESOURCE_NAME}-search-lb-${idx}-cert"
  local client_cert_name="${SEARCH_TLS_CERT_SECRET_PREFIX}-${SEARCH_RESOURCE_NAME}-search-lb-${idx}-client-cert"

  kubectl apply --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${deployment_name}
spec:
  secretName: ${server_cert_name}
  duration: 8760h    # 1 year
  renewBefore: 720h  # 30 days
  privateKey:
    algorithm: RSA
    size: 2048
  usages:
    - server auth
  dnsNames:
    - "${proxy_svc_0}"
    - "${proxy_svc_1}"
    - "${proxy_svc_2}"
  issuerRef:
    name: ${MDB_TLS_CA_ISSUER}
    kind: ClusterIssuer
EOF

  kubectl apply --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${deployment_name}-client
spec:
  secretName: ${client_cert_name}
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

  kubectl wait --for=condition=Ready certificate/"${deployment_name}" -n "${MDB_NAMESPACE}" --context "${K8S_CLUSTER_0_CONTEXT_NAME}" --timeout=60s
  kubectl wait --for=condition=Ready certificate/"${deployment_name}-client" -n "${MDB_NAMESPACE}" --context "${K8S_CLUSTER_0_CONTEXT_NAME}" --timeout=60s

  echo "  [ok] LB certificates for index ${idx} issued on ${K8S_CLUSTER_0_CONTEXT_NAME} (server=${server_cert_name}, client=${client_cert_name})"
}

copy_lb_secrets_to_owner() {
  local owner_ctx=$1
  local idx=$2

  # Index 0 is issued directly on its owning cluster (cluster 0) -- nothing to copy.
  [[ "${owner_ctx}" == "${K8S_CLUSTER_0_CONTEXT_NAME}" ]] && return 0

  local server_cert_name="${SEARCH_TLS_CERT_SECRET_PREFIX}-${SEARCH_RESOURCE_NAME}-search-lb-${idx}-cert"
  local client_cert_name="${SEARCH_TLS_CERT_SECRET_PREFIX}-${SEARCH_RESOURCE_NAME}-search-lb-${idx}-client-cert"

  for secret_name in "${server_cert_name}" "${client_cert_name}"; do
    kubectl get secret "${secret_name}" -n "${MDB_NAMESPACE}" --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -o json \
      | jq 'del(.metadata.uid, .metadata.resourceVersion, .metadata.creationTimestamp, .metadata.selfLink, .metadata.managedFields, .metadata.ownerReferences, .status)' \
      | kubectl apply --context "${owner_ctx}" -n "${MDB_NAMESPACE}" -f -
  done

  echo "  [ok] LB certificates for index ${idx} replicated to owning cluster ${owner_ctx}"
}

issue_lb_certs_for_index "${SEARCH_CLUSTER_0_INDEX}"
issue_lb_certs_for_index "${SEARCH_CLUSTER_1_INDEX}"
issue_lb_certs_for_index "${SEARCH_CLUSTER_2_INDEX}"

copy_lb_secrets_to_owner "${K8S_CLUSTER_0_CONTEXT_NAME}" "${SEARCH_CLUSTER_0_INDEX}"
copy_lb_secrets_to_owner "${K8S_CLUSTER_1_CONTEXT_NAME}" "${SEARCH_CLUSTER_1_INDEX}"
copy_lb_secrets_to_owner "${K8S_CLUSTER_2_CONTEXT_NAME}" "${SEARCH_CLUSTER_2_INDEX}"
