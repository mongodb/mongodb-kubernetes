# Generate TLS certificates for sharded cluster and MongoDBSearch
#
# For sharded clusters, the operator expects separate certificates for each component:
# - certs-<name>-mongos-cert for mongos pods
# - certs-<name>-config-cert for config server pods
# - certs-<name>-<shard>-cert for each shard's mongod pods
# - MongoDBSearch (mongot) pods - one per shard for external source mode

render_dns_list() {
  local dns_list=("$@")
  for dns in "${dns_list[@]}"; do
    printf "      - \"%s\"\n" "${dns}"
  done
}

# First, create the TLS CA ConfigMap for the MongoDB cluster
TMP_CA_CERT="$(mktemp)"
trap 'rm -f "${TMP_CA_CERT}"' EXIT

kubectl --context "${K8S_CTX}" get secret "${MDB_TLS_CA_SECRET_NAME}" -n "${MDB_NS}" \
  -o jsonpath="{.data['ca\\.crt']}" | base64 --decode > "${TMP_CA_CERT}"

kubectl --context "${K8S_CTX}" create configmap "${MDB_TLS_CA_CONFIGMAP}" -n "${MDB_NS}" \
  --from-file=ca-pem="${TMP_CA_CERT}" --from-file=mms-ca.crt="${TMP_CA_CERT}" \
  --from-file=ca.crt="${TMP_CA_CERT}" \
  --dry-run=client -o yaml | kubectl --context "${K8S_CTX}" apply -f -

# Create certificate for mongos
mongos_dns_names=()
for ((member = 0; member < MDB_MONGOS_COUNT; member++)); do
  mongos_dns_names+=("${MDB_EXTERNAL_CLUSTER_NAME}-mongos-${member}")
  mongos_dns_names+=("${MDB_EXTERNAL_CLUSTER_NAME}-mongos-${member}.${MDB_EXTERNAL_CLUSTER_NAME}-svc.${MDB_NS}.svc.cluster.local")
done
mongos_dns_names+=(
  "${MDB_EXTERNAL_CLUSTER_NAME}-svc.${MDB_NS}.svc.cluster.local"
  "*.${MDB_EXTERNAL_CLUSTER_NAME}-svc.${MDB_NS}.svc.cluster.local"
)

kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF_MANIFEST
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${MDB_EXTERNAL_CLUSTER_NAME}-mongos-tls
  namespace: ${MDB_NS}
spec:
  secretName: ${MDB_TLS_CERT_SECRET_PREFIX}-${MDB_EXTERNAL_CLUSTER_NAME}-mongos-cert
  issuerRef:
    name: ${MDB_TLS_CA_ISSUER}
    kind: ClusterIssuer
  duration: 240h0m0s
  renewBefore: 120h0m0s
  usages:
    - digital signature
    - key encipherment
    - server auth
    - client auth
  dnsNames:
$(render_dns_list "${mongos_dns_names[@]}")
EOF_MANIFEST

# Create certificate for config servers
config_dns_names=()
for ((member = 0; member < MDB_CONFIG_SERVER_COUNT; member++)); do
  config_dns_names+=("${MDB_EXTERNAL_CLUSTER_NAME}-config-${member}")
  config_dns_names+=("${MDB_EXTERNAL_CLUSTER_NAME}-config-${member}.${MDB_EXTERNAL_CLUSTER_NAME}-cs.${MDB_NS}.svc.cluster.local")
done
config_dns_names+=(
  "${MDB_EXTERNAL_CLUSTER_NAME}-cs.${MDB_NS}.svc.cluster.local"
  "*.${MDB_EXTERNAL_CLUSTER_NAME}-cs.${MDB_NS}.svc.cluster.local"
)

kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF_MANIFEST
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${MDB_EXTERNAL_CLUSTER_NAME}-config-tls
  namespace: ${MDB_NS}
spec:
  secretName: ${MDB_TLS_CERT_SECRET_PREFIX}-${MDB_EXTERNAL_CLUSTER_NAME}-config-cert
  issuerRef:
    name: ${MDB_TLS_CA_ISSUER}
    kind: ClusterIssuer
  duration: 240h0m0s
  renewBefore: 120h0m0s
  usages:
    - digital signature
    - key encipherment
    - server auth
    - client auth
  dnsNames:
$(render_dns_list "${config_dns_names[@]}")
EOF_MANIFEST

# Create certificate for each shard
for ((shard = 0; shard < MDB_SHARD_COUNT; shard++)); do
  shard_dns_names=()
  for ((member = 0; member < MDB_MONGODS_PER_SHARD; member++)); do
    shard_dns_names+=("${MDB_EXTERNAL_CLUSTER_NAME}-${shard}-${member}")
    shard_dns_names+=("${MDB_EXTERNAL_CLUSTER_NAME}-${shard}-${member}.${MDB_EXTERNAL_CLUSTER_NAME}-sh.${MDB_NS}.svc.cluster.local")
  done
  shard_dns_names+=(
    "${MDB_EXTERNAL_CLUSTER_NAME}-sh.${MDB_NS}.svc.cluster.local"
    "*.${MDB_EXTERNAL_CLUSTER_NAME}-sh.${MDB_NS}.svc.cluster.local"
  )

  kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF_MANIFEST
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${MDB_EXTERNAL_CLUSTER_NAME}-${shard}-tls
  namespace: ${MDB_NS}
spec:
  secretName: ${MDB_TLS_CERT_SECRET_PREFIX}-${MDB_EXTERNAL_CLUSTER_NAME}-${shard}-cert
  issuerRef:
    name: ${MDB_TLS_CA_ISSUER}
    kind: ClusterIssuer
  duration: 240h0m0s
  renewBefore: 120h0m0s
  usages:
    - digital signature
    - key encipherment
    - server auth
    - client auth
  dnsNames:
$(render_dns_list "${shard_dns_names[@]}")
EOF_MANIFEST
done

# Create per-shard certificates for MongoDBSearch (mongot)
# Each shard gets its own certificate following the pattern: {prefix}-{shardName}-search-cert
# This enables per-shard TLS where each mongot StatefulSet uses its own unique certificate
for ((shard = 0; shard < MDB_SHARD_COUNT; shard++)); do
  shard_name="${MDB_EXTERNAL_CLUSTER_NAME}-${shard}"

  # Build DNS names for this shard's mongot services
  shard_search_dns_names=(
    "${MDB_SEARCH_RESOURCE_NAME}-mongot-${shard_name}-svc.${MDB_NS}.svc.cluster.local"
    "*.${MDB_SEARCH_RESOURCE_NAME}-mongot-${shard_name}-svc.${MDB_NS}.svc.cluster.local"
  )

  kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF_MANIFEST
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${shard_name}-search-tls
  namespace: ${MDB_NS}
spec:
  secretName: ${MDB_SEARCH_TLS_CERT_PREFIX}-${shard_name}-search-cert
  issuerRef:
    name: ${MDB_TLS_CA_ISSUER}
    kind: ClusterIssuer
  duration: 240h0m0s
  renewBefore: 120h0m0s
  usages:
    - digital signature
    - key encipherment
    - server auth
    - client auth
  dnsNames:
$(render_dns_list "${shard_search_dns_names[@]}")
EOF_MANIFEST
done

# Wait for all certificates to be ready
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" wait --for=condition=Ready \
  certificate "${MDB_EXTERNAL_CLUSTER_NAME}-mongos-tls" --timeout=300s
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" wait --for=condition=Ready \
  certificate "${MDB_EXTERNAL_CLUSTER_NAME}-config-tls" --timeout=300s
for ((shard = 0; shard < MDB_SHARD_COUNT; shard++)); do
  # Wait for shard mongod TLS certificate
  kubectl --context "${K8S_CTX}" -n "${MDB_NS}" wait --for=condition=Ready \
    certificate "${MDB_EXTERNAL_CLUSTER_NAME}-${shard}-tls" --timeout=300s
  # Wait for per-shard search TLS certificate
  kubectl --context "${K8S_CTX}" -n "${MDB_NS}" wait --for=condition=Ready \
    certificate "${MDB_EXTERNAL_CLUSTER_NAME}-${shard}-search-tls" --timeout=300s
done

echo "TLS certificates created for sharded cluster and MongoDBSearch (per-shard)"
