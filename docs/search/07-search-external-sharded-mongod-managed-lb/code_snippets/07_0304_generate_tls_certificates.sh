#!/usr/bin/env bash
# Generate TLS certificates for the simulated external MongoDB cluster
#
# This creates server certificates for:
# - MongoDB shards (each shard needs its own certificate)
# - MongoDB config servers
# - MongoDB mongos routers
#
# The certificates are signed by our CA and include proper SANs for in-cluster DNS.
#
# ============================================================================
# TLS CERTIFICATE NAMING CONVENTION FOR MONGODB
# ============================================================================
# The MongoDB Enterprise operator expects certificates with specific names:
#
# Shards:         {prefix}-{cluster}-{shard-index}-cert
#                 Example: certs-ext-mdb-sh-0-cert, certs-ext-mdb-sh-1-cert
#
# Config servers: {prefix}-{cluster}-config-cert
#                 Example: certs-ext-mdb-sh-config-cert
#
# Mongos:         {prefix}-{cluster}-mongos-cert
#                 Example: certs-ext-mdb-sh-mongos-cert
#
# DNS names in certificates follow Kubernetes service DNS patterns:
#   {pod-name}.{headless-service}.{namespace}.svc.cluster.local
# ============================================================================
# DEPENDS ON: 07_0302_configure_tls_prerequisites.sh (CA issuer must exist)
# ============================================================================

echo "Generating TLS certificates for MongoDB..."

# Creates a cert-manager Certificate resource
create_mongo_cert() {
  local name="$1"
  local dns_names="$2"

  kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${name}
spec:
  secretName: ${name}
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
}

# ============================================================================
# SHARD CERTIFICATES
# ============================================================================
# For a 2-shard cluster, this creates:
#   - certs-ext-mdb-sh-0-cert (for shard 0)
#   - certs-ext-mdb-sh-1-cert (for shard 1)
# ============================================================================

echo "Creating shard certificates..."
for ((shard = 0; shard < MDB_SHARD_COUNT; shard++)); do
  shard_name="${MDB_EXTERNAL_CLUSTER_NAME}-${shard}"
  cert_name="${MDB_TLS_CERT_SECRET_PREFIX}-${shard_name}-cert"

  dns_names=""
  for ((member = 0; member < MDB_MONGODS_PER_SHARD; member++)); do
    dns_names="${dns_names}    - ${shard_name}-${member}.${MDB_EXTERNAL_CLUSTER_NAME}-sh.${MDB_NS}.svc.cluster.local
"
  done
  # Add wildcard for flexibility (handles replica additions without new certs)
  dns_names="${dns_names}    - \"*.${MDB_EXTERNAL_CLUSTER_NAME}-sh.${MDB_NS}.svc.cluster.local\""

  create_mongo_cert "${cert_name}" "${dns_names}"
  echo "  ✓ Certificate requested for shard ${shard_name}"
done

# ============================================================================
# CONFIG SERVER CERTIFICATE
# ============================================================================
# Config servers store cluster metadata. They use a separate headless service.
# Certificate name: certs-ext-mdb-sh-config-cert
# DNS names: ext-mdb-sh-config-{i}.ext-mdb-sh-cs.mongodb.svc.cluster.local
# ============================================================================

echo "Creating config server certificate..."
config_cert_name="${MDB_TLS_CERT_SECRET_PREFIX}-${MDB_EXTERNAL_CLUSTER_NAME}-config-cert"
config_dns_names=""
for ((i = 0; i < MDB_CONFIG_SERVER_COUNT; i++)); do
  # DNS format: {cluster}-config-{i}.{cluster}-cs.{namespace}.svc.cluster.local
  config_dns_names="${config_dns_names}    - ${MDB_EXTERNAL_CLUSTER_NAME}-config-${i}.${MDB_EXTERNAL_CLUSTER_NAME}-cs.${MDB_NS}.svc.cluster.local
"
done
config_dns_names="${config_dns_names}    - \"*.${MDB_EXTERNAL_CLUSTER_NAME}-cs.${MDB_NS}.svc.cluster.local\""

create_mongo_cert "${config_cert_name}" "${config_dns_names}"
echo "  ✓ Certificate requested for config servers"

# ============================================================================
# MONGOS CERTIFICATE
# ============================================================================
# Mongos routers are the entry point for sharded cluster operations.
# Certificate name: certs-ext-mdb-sh-mongos-cert
# DNS names: ext-mdb-sh-mongos-{i}.ext-mdb-sh-svc.mongodb.svc.cluster.local
# ============================================================================

echo "Creating mongos certificate..."
mongos_cert_name="${MDB_TLS_CERT_SECRET_PREFIX}-${MDB_EXTERNAL_CLUSTER_NAME}-mongos-cert"
mongos_dns_names=""
for ((i = 0; i < MDB_MONGOS_COUNT; i++)); do
  # DNS format: {cluster}-mongos-{i}.{cluster}-svc.{namespace}.svc.cluster.local
  mongos_dns_names="${mongos_dns_names}    - ${MDB_EXTERNAL_CLUSTER_NAME}-mongos-${i}.${MDB_EXTERNAL_CLUSTER_NAME}-svc.${MDB_NS}.svc.cluster.local
"
done
mongos_dns_names="${mongos_dns_names}    - \"*.${MDB_EXTERNAL_CLUSTER_NAME}-svc.${MDB_NS}.svc.cluster.local\""

create_mongo_cert "${mongos_cert_name}" "${mongos_dns_names}"
echo "  ✓ Certificate requested for mongos"

# ============================================================================
# WAIT FOR ALL CERTIFICATES
# ============================================================================
echo ""
echo "Waiting for all certificates to be ready..."
for ((shard = 0; shard < MDB_SHARD_COUNT; shard++)); do
  shard_name="${MDB_EXTERNAL_CLUSTER_NAME}-${shard}"
  cert_name="${MDB_TLS_CERT_SECRET_PREFIX}-${shard_name}-cert"
  kubectl wait --for=condition=Ready certificate/${cert_name} -n "${MDB_NS}" --context "${K8S_CTX}" --timeout=60s
done
kubectl wait --for=condition=Ready certificate/${config_cert_name} -n "${MDB_NS}" --context "${K8S_CTX}" --timeout=60s
kubectl wait --for=condition=Ready certificate/${mongos_cert_name} -n "${MDB_NS}" --context "${K8S_CTX}" --timeout=60s

echo ""
echo "✓ All MongoDB TLS certificates created"
echo ""
echo "Summary of certificates created:"
echo "  - Shard certificates: ${MDB_SHARD_COUNT}"
echo "  - Config server certificate: ${config_cert_name}"
echo "  - Mongos certificate: ${mongos_cert_name}"
