#!/usr/bin/env bash
# Generate TLS certificates for MongoDB shards, config servers, and mongos

echo "Generating TLS certificates for MongoDB..."

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

# Shard certificates
echo "Creating shard certificates..."
for ((shard = 0; shard < MDB_SHARD_COUNT; shard++)); do
  shard_name="${MDB_EXTERNAL_CLUSTER_NAME}-${shard}"
  cert_name="${MDB_TLS_CERT_SECRET_PREFIX}-${shard_name}-cert"

  dns_names=""
  for ((member = 0; member < MDB_MONGODS_PER_SHARD; member++)); do
    dns_names="${dns_names}    - ${shard_name}-${member}.${MDB_EXTERNAL_CLUSTER_NAME}-sh.${MDB_NS}.svc.cluster.local
"
  done
  dns_names="${dns_names}    - \"*.${MDB_EXTERNAL_CLUSTER_NAME}-sh.${MDB_NS}.svc.cluster.local\""

  create_mongo_cert "${cert_name}" "${dns_names}"
  echo "  ✓ Certificate requested for shard ${shard_name}"
done

# Config server certificate
echo "Creating config server certificate..."
config_cert_name="${MDB_TLS_CERT_SECRET_PREFIX}-${MDB_EXTERNAL_CLUSTER_NAME}-config-cert"
config_dns_names=""
for ((i = 0; i < MDB_CONFIG_SERVER_COUNT; i++)); do
  config_dns_names="${config_dns_names}    - ${MDB_EXTERNAL_CLUSTER_NAME}-config-${i}.${MDB_EXTERNAL_CLUSTER_NAME}-cs.${MDB_NS}.svc.cluster.local
"
done
config_dns_names="${config_dns_names}    - \"*.${MDB_EXTERNAL_CLUSTER_NAME}-cs.${MDB_NS}.svc.cluster.local\""

create_mongo_cert "${config_cert_name}" "${config_dns_names}"
echo "  ✓ Certificate requested for config servers"

# Mongos certificate
echo "Creating mongos certificate..."
mongos_cert_name="${MDB_TLS_CERT_SECRET_PREFIX}-${MDB_EXTERNAL_CLUSTER_NAME}-mongos-cert"
mongos_dns_names=""
for ((i = 0; i < MDB_MONGOS_COUNT; i++)); do
  mongos_dns_names="${mongos_dns_names}    - ${MDB_EXTERNAL_CLUSTER_NAME}-mongos-${i}.${MDB_EXTERNAL_CLUSTER_NAME}-svc.${MDB_NS}.svc.cluster.local
"
done
mongos_dns_names="${mongos_dns_names}    - \"*.${MDB_EXTERNAL_CLUSTER_NAME}-svc.${MDB_NS}.svc.cluster.local\""

create_mongo_cert "${mongos_cert_name}" "${mongos_dns_names}"
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
