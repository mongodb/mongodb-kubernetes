#!/usr/bin/env bash
# Generate TLS certificates for MongoDB shards, config servers, and mongos
#
# DNS naming pattern: <pod>-<ordinal>.<headless-svc>.<namespace>.svc.cluster.local

source "code_snippets/_tls_helpers.sh"

echo "Generating TLS certificates for MongoDB..."

# Shard certificates — one cert per shard covering all members
echo "Creating shard certificates..."
for ((shard = 0; shard < MDB_SHARD_COUNT; shard++)); do
  shard_name="${MDB_EXTERNAL_CLUSTER_NAME}-${shard}"
  cert_name="${MDB_TLS_CERT_SECRET_PREFIX}-${shard_name}-cert"
  dns_names=$(build_dns_names "${MDB_MONGODS_PER_SHARD}" "${shard_name}" "${MDB_EXTERNAL_CLUSTER_NAME}-sh")
  create_cert "${cert_name}" "${dns_names}"
  echo "  ✓ Certificate requested for shard ${shard_name}"
done

# Config server certificate
echo "Creating config server certificate..."
config_cert_name="${MDB_TLS_CERT_SECRET_PREFIX}-${MDB_EXTERNAL_CLUSTER_NAME}-config-cert"
config_dns=$(build_dns_names "${MDB_CONFIG_SERVER_COUNT}" "${MDB_EXTERNAL_CLUSTER_NAME}-config" "${MDB_EXTERNAL_CLUSTER_NAME}-cs")
create_cert "${config_cert_name}" "${config_dns}"
echo "  ✓ Certificate requested for config servers"

# Mongos certificate
echo "Creating mongos certificate..."
mongos_cert_name="${MDB_TLS_CERT_SECRET_PREFIX}-${MDB_EXTERNAL_CLUSTER_NAME}-mongos-cert"
mongos_dns=$(build_dns_names "${MDB_MONGOS_COUNT}" "${MDB_EXTERNAL_CLUSTER_NAME}-mongos" "${MDB_EXTERNAL_CLUSTER_NAME}-svc")
create_cert "${mongos_cert_name}" "${mongos_dns}"
echo "  ✓ Certificate requested for mongos"

# Wait for all certificates
echo ""
echo "Waiting for all certificates to be ready..."
for ((shard = 0; shard < MDB_SHARD_COUNT; shard++)); do
  shard_name="${MDB_EXTERNAL_CLUSTER_NAME}-${shard}"
  cert_name="${MDB_TLS_CERT_SECRET_PREFIX}-${shard_name}-cert"
  kubectl wait --for=condition=Ready certificate/"${cert_name}" -n "${MDB_NS}" --context "${K8S_CTX}" --timeout=60s
done
kubectl wait --for=condition=Ready certificate/"${config_cert_name}" -n "${MDB_NS}" --context "${K8S_CTX}" --timeout=60s
kubectl wait --for=condition=Ready certificate/"${mongos_cert_name}" -n "${MDB_NS}" --context "${K8S_CTX}" --timeout=60s

echo "✓ All MongoDB TLS certificates created"
