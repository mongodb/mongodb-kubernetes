#!/usr/bin/env bash
# Create TLS certificates for MongoDB Search (mongot) pods

echo "Creating TLS certificates for MongoDB Search (mongot) pods..."

create_search_cert() {
  local shard_name="$1"
  local cert_name="${MDB_SEARCH_TLS_CERT_PREFIX}-${MDB_SEARCH_RESOURCE_NAME}-search-0-${shard_name}-cert"
  local sts_name="${MDB_SEARCH_RESOURCE_NAME}-search-0-${shard_name}"

  local dns_names=""
  for ((i = 0; i < MDB_MONGOT_REPLICAS; i++)); do
    dns_names="${dns_names}    - ${sts_name}-${i}.${MDB_SEARCH_RESOURCE_NAME}-search-0-${shard_name}-svc.${MDB_NS}.svc.cluster.local
"
  done
  dns_names="${dns_names}    - \"*.${MDB_SEARCH_RESOURCE_NAME}-search-0-${shard_name}-svc.${MDB_NS}.svc.cluster.local\""

  echo "  Creating certificate: ${cert_name}"

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

echo "Creating certificates for ${MDB_SHARD_COUNT} shards with ${MDB_MONGOT_REPLICAS} replicas each..."

for ((shard = 0; shard < MDB_SHARD_COUNT; shard++)); do
  shard_name="${MDB_EXTERNAL_CLUSTER_NAME}-${shard}"
  create_search_cert "${shard_name}"
done

echo "Waiting for mongot certificates to be ready..."
for ((shard = 0; shard < MDB_SHARD_COUNT; shard++)); do
  shard_name="${MDB_EXTERNAL_CLUSTER_NAME}-${shard}"
  cert_name="${MDB_SEARCH_TLS_CERT_PREFIX}-${MDB_SEARCH_RESOURCE_NAME}-search-0-${shard_name}-cert"
  kubectl wait --for=condition=Ready certificate/"${cert_name}" \
    -n "${MDB_NS}" \
    --context "${K8S_CTX}" \
    --timeout=60s
done

echo "✓ All MongoDB Search (mongot) TLS certificates created"
