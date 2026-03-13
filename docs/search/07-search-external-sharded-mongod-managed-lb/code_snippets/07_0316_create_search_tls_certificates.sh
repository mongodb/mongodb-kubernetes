#!/usr/bin/env bash
# Create TLS certificates for MongoDB Search (mongot)
#
# For sharded clusters, each shard's mongot needs its own TLS certificate.
# The naming convention is: {prefix}-{searchName}-search-0-{shardName}-cert
# Example: certs-ext-search-search-0-ext-mdb-sh-0-cert
#
# With managed LB, the operator also creates certificates for Envoy automatically,
# so you only need to create the mongot certificates here.

echo "Creating TLS certificates for MongoDB Search..."

# Function to create per-shard search certificate
# Naming convention: {prefix}-{searchName}-search-0-{shardName}-cert
create_search_cert() {
  local shard_name="$1"
  # Correct naming format per TLSSecretForShard() API method
  local cert_name="${MDB_SEARCH_TLS_CERT_PREFIX}-${MDB_SEARCH_RESOURCE_NAME}-search-0-${shard_name}-cert"

  # The STS name follows the pattern: {search-name}-search-0-{shard-name}
  local sts_name="${MDB_SEARCH_RESOURCE_NAME}-search-0-${shard_name}"

  # Build DNS names for all mongot replicas in this shard
  local dns_names=""
  for ((i = 0; i < MDB_MONGOT_REPLICAS; i++)); do
    dns_names="${dns_names}    - ${sts_name}-${i}.${MDB_SEARCH_RESOURCE_NAME}-search-0-${shard_name}-svc.${MDB_NS}.svc.cluster.local
"
  done
  # Add wildcard for flexibility
  dns_names="${dns_names}    - \"*.${MDB_SEARCH_RESOURCE_NAME}-search-0-${shard_name}-svc.${MDB_NS}.svc.cluster.local\""

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

# Create certificate for each shard
for ((shard = 0; shard < MDB_SHARD_COUNT; shard++)); do
  shard_name="${MDB_EXTERNAL_CLUSTER_NAME}-${shard}"
  create_search_cert "${shard_name}"
done

# Wait for all certificates to be ready
echo "Waiting for certificates to be ready..."
for ((shard = 0; shard < MDB_SHARD_COUNT; shard++)); do
  shard_name="${MDB_EXTERNAL_CLUSTER_NAME}-${shard}"
  cert_name="${MDB_SEARCH_TLS_CERT_PREFIX}-${MDB_SEARCH_RESOURCE_NAME}-search-0-${shard_name}-cert"
  kubectl wait --for=condition=Ready certificate/${cert_name} \
    -n "${MDB_NS}" \
    --context "${K8S_CTX}" \
    --timeout=60s
done

echo "✓ All MongoDB Search TLS certificates created"
echo ""
echo "Note: The operator will automatically create Envoy TLS certificates"
echo "when the MongoDBSearch resource with lb.mode: Managed is applied."
