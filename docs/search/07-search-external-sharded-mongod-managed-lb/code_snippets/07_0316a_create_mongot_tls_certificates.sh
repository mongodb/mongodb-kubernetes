#!/usr/bin/env bash
# Create TLS certificates for MongoDB Search (mongot) pods
#
# "search-0" = first (and only) search deployment; the operator names
# StatefulSets as {resource}-search-{index}-{shard}

source "code_snippets/_tls_helpers.sh"

echo "Creating TLS certificates for MongoDB Search (mongot) pods..."
echo "Creating certificates for ${MDB_SHARD_COUNT} shards with ${MDB_MONGOT_REPLICAS} replicas each..."

for ((shard = 0; shard < MDB_SHARD_COUNT; shard++)); do
  shard_name="${MDB_EXTERNAL_CLUSTER_NAME}-${shard}"
  sts_name="${MDB_SEARCH_RESOURCE_NAME}-search-0-${shard_name}"
  cert_name="${MDB_TLS_CERT_SECRET_PREFIX}-${sts_name}-cert"

  dns_names=$(build_dns_names "${MDB_MONGOT_REPLICAS}" "${sts_name}" "${sts_name}-svc")
  echo "  Creating certificate: ${cert_name}"
  create_cert "${cert_name}" "${dns_names}"
  echo "  ✓ Certificate requested: ${cert_name}"
done

echo "Waiting for mongot certificates to be ready..."
for ((shard = 0; shard < MDB_SHARD_COUNT; shard++)); do
  shard_name="${MDB_EXTERNAL_CLUSTER_NAME}-${shard}"
  cert_name="${MDB_TLS_CERT_SECRET_PREFIX}-${MDB_SEARCH_RESOURCE_NAME}-search-0-${shard_name}-cert"
  kubectl wait --for=condition=Ready certificate/"${cert_name}" \
    -n "${MDB_NS}" \
    --context "${K8S_CTX}" \
    --timeout=60s
done

echo "✓ All MongoDB Search (mongot) TLS certificates created"
