#!/usr/bin/env bash
# Create TLS certificates for the managed load balancer (Envoy proxy)
#
# ============================================================================
# TLS CERTIFICATE NAMING CONVENTION FOR ENVOY (MANAGED LB)
# ============================================================================
# When using lb.mode: Managed, the operator deploys an Envoy proxy that needs:
#
# 1. Server certificate: {prefix}-{search-name}-search-lb-cert
#    - Used by Envoy to accept incoming TLS connections from mongod
#    - DNS names include all per-shard proxy service hostnames
#
# 2. Client certificate: {prefix}-{search-name}-search-lb-client-cert
#    - Used by Envoy to make TLS connections to mongot backends
#    - Uses wildcard DNS for flexibility
#
# IMPORTANT: These must be created BEFORE the MongoDBSearch resource is applied!
# ============================================================================
# DEPENDS ON:
#   - 07_0302_configure_tls_prerequisites.sh (CA issuer must exist)
#   - 07_0316a_create_mongot_tls_certificates.sh (should run first)
# ============================================================================

echo "Creating TLS certificates for managed load balancer (Envoy)..."

# Certificate names follow the operator's naming convention
lb_server_cert="${MDB_SEARCH_TLS_CERT_PREFIX}-${MDB_SEARCH_RESOURCE_NAME}-search-lb-cert"
lb_client_cert="${MDB_SEARCH_TLS_CERT_PREFIX}-${MDB_SEARCH_RESOURCE_NAME}-search-lb-client-cert"

echo "  Server cert: ${lb_server_cert}"
echo "  Client cert: ${lb_client_cert}"
echo ""

# ============================================================================
# BUILD DNS NAMES FOR LB SERVER CERTIFICATE
# ============================================================================
# The server certificate needs DNS names for all per-shard proxy services.
# For a 2-shard cluster, this includes:
#   - ext-search-search-0-ext-mdb-sh-0-proxy-svc.mongodb.svc.cluster.local
#   - ext-search-search-0-ext-mdb-sh-1-proxy-svc.mongodb.svc.cluster.local
# ============================================================================

echo "Building DNS names for LB server certificate..."
lb_dns_names=""
for ((shard = 0; shard < MDB_SHARD_COUNT; shard++)); do
  shard_name="${MDB_EXTERNAL_CLUSTER_NAME}-${shard}"
  # Proxy service name format: {search-name}-search-0-{shard-name}-proxy-svc
  proxy_svc="${MDB_SEARCH_RESOURCE_NAME}-search-0-${shard_name}-proxy-svc"
  lb_dns_names="${lb_dns_names}    - ${proxy_svc}.${MDB_NS}.svc.cluster.local
"
  echo "    - ${proxy_svc}.${MDB_NS}.svc.cluster.local"
done
# Add wildcard for flexibility
lb_dns_names="${lb_dns_names}    - \"*.${MDB_NS}.svc.cluster.local\""
echo "    - *.${MDB_NS}.svc.cluster.local (wildcard)"
echo ""

# Create LB server certificate
echo "Creating LB server certificate..."
kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${lb_server_cert}
spec:
  secretName: ${lb_server_cert}
  duration: 8760h  # 1 year
  renewBefore: 720h  # 30 days
  privateKey:
    algorithm: RSA
    size: 2048
  usages:
    - server auth
    - client auth
  dnsNames:
${lb_dns_names}
  issuerRef:
    name: ${MDB_TLS_CA_ISSUER}
    kind: ClusterIssuer
EOF
echo "  ✓ LB server certificate requested: ${lb_server_cert}"
echo ""

# Create LB client certificate
echo "Creating LB client certificate..."
kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${lb_client_cert}
spec:
  secretName: ${lb_client_cert}
  duration: 8760h  # 1 year
  renewBefore: 720h  # 30 days
  privateKey:
    algorithm: RSA
    size: 2048
  usages:
    - client auth
  dnsNames:
    # Wildcard covers all services in the namespace
    - "*.${MDB_NS}.svc.cluster.local"
  issuerRef:
    name: ${MDB_TLS_CA_ISSUER}
    kind: ClusterIssuer
EOF
echo "  ✓ LB client certificate requested: ${lb_client_cert}"
echo ""

# Wait for LB certificates to be ready
echo "Waiting for LB certificates to be ready..."
kubectl wait --for=condition=Ready certificate/${lb_server_cert} \
  -n "${MDB_NS}" \
  --context "${K8S_CTX}" \
  --timeout=60s
kubectl wait --for=condition=Ready certificate/${lb_client_cert} \
  -n "${MDB_NS}" \
  --context "${K8S_CTX}" \
  --timeout=60s

echo ""
echo "✓ All managed load balancer (Envoy) TLS certificates created"
echo ""
echo "Summary of certificates created:"
echo "  - ${lb_server_cert} (for incoming mongod connections)"
echo "  - ${lb_client_cert} (for outgoing mongot connections)"

