#!/usr/bin/env bash
# Generate TLS certificates for MongoDB shards, config servers, and mongos
#
# DNS naming pattern: <pod>-<ordinal>.<headless-svc>.<namespace>.svc.cluster.local

echo "Generating TLS certificates for MongoDB..."

# Shard certificates — one cert per shard covering all members
echo "Creating shard certificates..."
for shard_name in ${MDB_EXTERNAL_SHARD_NAMES}; do
  cert_name="${MDB_TLS_CERT_SECRET_PREFIX}-${shard_name}-cert"

  dns_names=""
  for ((i = 0; i < MDB_MONGODS_PER_SHARD; i++)); do
    dns_names="${dns_names}    - ${shard_name}-${i}.${MDB_EXTERNAL_SHARD_SVC}.${MDB_NS}.svc.cluster.local
"
  done
  dns_names="${dns_names}    - \"*.${MDB_EXTERNAL_SHARD_SVC}.${MDB_NS}.svc.cluster.local\""

  kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${cert_name}
spec:
  secretName: ${cert_name}
  duration: 8760h    # 1 year
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
  echo "  ✓ Certificate requested for shard ${shard_name}"
done

# Config server certificate
echo "Creating config server certificate..."
config_cert_name="${MDB_TLS_CERT_SECRET_PREFIX}-${MDB_EXTERNAL_CONFIG_RS_NAME}-cert"

config_dns=""
for ((i = 0; i < MDB_CONFIG_SERVER_COUNT; i++)); do
  config_dns="${config_dns}    - ${MDB_EXTERNAL_CONFIG_RS_NAME}-${i}.${MDB_EXTERNAL_CONFIG_SVC}.${MDB_NS}.svc.cluster.local
"
done
config_dns="${config_dns}    - \"*.${MDB_EXTERNAL_CONFIG_SVC}.${MDB_NS}.svc.cluster.local\""

kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${config_cert_name}
spec:
  secretName: ${config_cert_name}
  duration: 8760h    # 1 year
  renewBefore: 720h  # 30 days
  privateKey:
    algorithm: RSA
    size: 2048
  usages:
    - server auth
    - client auth
  dnsNames:
${config_dns}
  issuerRef:
    name: ${MDB_TLS_CA_ISSUER}
    kind: ClusterIssuer
EOF
echo "  ✓ Certificate requested for config servers"

# Mongos certificate
echo "Creating mongos certificate..."
mongos_cert_name="${MDB_TLS_CERT_SECRET_PREFIX}-${MDB_EXTERNAL_MONGOS_NAME}-cert"

mongos_dns=""
for ((i = 0; i < MDB_MONGOS_COUNT; i++)); do
  mongos_dns="${mongos_dns}    - ${MDB_EXTERNAL_MONGOS_NAME}-${i}.${MDB_EXTERNAL_MONGOS_SVC}.${MDB_NS}.svc.cluster.local
"
done
mongos_dns="${mongos_dns}    - \"*.${MDB_EXTERNAL_MONGOS_SVC}.${MDB_NS}.svc.cluster.local\""

kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${mongos_cert_name}
spec:
  secretName: ${mongos_cert_name}
  duration: 8760h    # 1 year
  renewBefore: 720h  # 30 days
  privateKey:
    algorithm: RSA
    size: 2048
  usages:
    - server auth
    - client auth
  dnsNames:
${mongos_dns}
  issuerRef:
    name: ${MDB_TLS_CA_ISSUER}
    kind: ClusterIssuer
EOF
echo "  ✓ Certificate requested for mongos"

# Wait for all certificates
echo ""
echo "Waiting for all certificates to be ready..."
for shard_name in ${MDB_EXTERNAL_SHARD_NAMES}; do
  cert_name="${MDB_TLS_CERT_SECRET_PREFIX}-${shard_name}-cert"
  kubectl wait --for=condition=Ready certificate/"${cert_name}" -n "${MDB_NS}" --context "${K8S_CTX}" --timeout=60s
done
kubectl wait --for=condition=Ready certificate/"${config_cert_name}" -n "${MDB_NS}" --context "${K8S_CTX}" --timeout=60s
kubectl wait --for=condition=Ready certificate/"${mongos_cert_name}" -n "${MDB_NS}" --context "${K8S_CTX}" --timeout=60s

echo "✓ All MongoDB TLS certificates created"
