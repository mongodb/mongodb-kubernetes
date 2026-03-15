#!/usr/bin/env bash
# Create TLS certificates for MongoDB Search (mongot) pods
#
# ============================================================================
# TLS CERTIFICATE NAMING CONVENTION FOR MONGOT
# ============================================================================
# For sharded clusters, each shard's mongot needs its own TLS certificate.
# The operator expects certificates with this exact naming pattern:
#
#   Secret name: {prefix}-{search-name}-search-0-{shard-name}-cert
#   Example:     certs-ext-search-search-0-ext-mdb-sh-0-cert
#
# DNS names in certificate (for each mongot replica):
#   {search-name}-search-0-{shard-name}-{replica}.{search-name}-search-0-{shard-name}-svc.{ns}.svc.cluster.local
#   Example: ext-search-search-0-ext-mdb-sh-0-0.ext-search-search-0-ext-mdb-sh-0-svc.mongodb.svc.cluster.local
#
# ============================================================================
# DEPENDS ON: 07_0302_configure_tls_prerequisites.sh (CA issuer must exist)
# ============================================================================

echo "Creating TLS certificates for MongoDB Search (mongot) pods..."

# Creates one certificate per shard with DNS names for all mongot replicas
create_search_cert() {
  local shard_name="$1"
  local cert_name="${MDB_SEARCH_TLS_CERT_PREFIX}-${MDB_SEARCH_RESOURCE_NAME}-search-0-${shard_name}-cert"
  local sts_name="${MDB_SEARCH_RESOURCE_NAME}-search-0-${shard_name}"

  local dns_names=""
  for ((i = 0; i < MDB_MONGOT_REPLICAS; i++)); do
    dns_names="${dns_names}    - ${sts_name}-${i}.${MDB_SEARCH_RESOURCE_NAME}-search-0-${shard_name}-svc.${MDB_NS}.svc.cluster.local
"
  done
  # Add wildcard for flexibility (handles future replica additions)
  dns_names="${dns_names}    - \"*.${MDB_SEARCH_RESOURCE_NAME}-search-0-${shard_name}-svc.${MDB_NS}.svc.cluster.local\""

  # Show what will be created
  echo "  Creating certificate: ${cert_name}"
  echo "    DNS names:"
  echo "${dns_names}" | sed 's/^/      /'

  kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${cert_name}
spec:
  secretName: ${cert_name}
  duration: 8760h  # 1 year
  renewBefore: 720h  # 30 days
  privateKey:
    algorithm: RSA
    size: 2048
  usages:
    - server auth
    - client auth
  dnsNames:
${dns_names}
  issuerRef:
    name: ${MDB_TLS_CA_ISSUER}
    kind: ClusterIssuer
EOF

  echo "  ✓ Certificate requested: ${cert_name}"
}

# ============================================================================
# MAIN LOOP: Create certificate for each shard
# ============================================================================
# For a 2-shard cluster, this creates:
#   - certs-ext-search-search-0-ext-mdb-sh-0-cert
#   - certs-ext-search-search-0-ext-mdb-sh-1-cert
# ============================================================================

echo ""
echo "Creating certificates for ${MDB_SHARD_COUNT} shards with ${MDB_MONGOT_REPLICAS} replicas each..."
echo ""

for ((shard = 0; shard < MDB_SHARD_COUNT; shard++)); do
  shard_name="${MDB_EXTERNAL_CLUSTER_NAME}-${shard}"
  create_search_cert "${shard_name}"
  echo ""
done

# Wait for all certificates to be ready
echo "Waiting for mongot certificates to be ready..."
for ((shard = 0; shard < MDB_SHARD_COUNT; shard++)); do
  shard_name="${MDB_EXTERNAL_CLUSTER_NAME}-${shard}"
  cert_name="${MDB_SEARCH_TLS_CERT_PREFIX}-${MDB_SEARCH_RESOURCE_NAME}-search-0-${shard_name}-cert"
  kubectl wait --for=condition=Ready certificate/${cert_name} \
    -n "${MDB_NS}" \
    --context "${K8S_CTX}" \
    --timeout=60s
done

echo ""
echo "✓ All MongoDB Search (mongot) TLS certificates created"
echo ""
echo "Next step: Run 07_0316b_create_lb_tls_certificates.sh to create Envoy certificates"
