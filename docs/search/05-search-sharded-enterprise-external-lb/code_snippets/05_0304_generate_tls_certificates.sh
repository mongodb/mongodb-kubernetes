# Generate TLS certificates for sharded cluster and MongoDBSearch
#
# For sharded clusters, the operator expects separate certificates for each component:
# - certs-<name>-mongos-cert for mongos pods
# - certs-<name>-config-cert for config server pods
# - certs-<name>-<shard>-cert for each shard's mongod pods
# - MongoDBSearch (mongot) pods - one per shard for external LB mode

render_dns_list() {
  local dns_list=("$@")
  for dns in "${dns_list[@]}"; do
    printf "      - \"%s\"\n" "${dns}"
  done
}

# Create certificate for mongos
mongos_dns_names=()
for ((member = 0; member < MDB_MONGOS_COUNT; member++)); do
  mongos_dns_names+=("${MDB_RESOURCE_NAME}-mongos-${member}")
  mongos_dns_names+=("${MDB_RESOURCE_NAME}-mongos-${member}.${MDB_RESOURCE_NAME}-svc.${MDB_NS}.svc.cluster.local")
done
mongos_dns_names+=(
  "${MDB_RESOURCE_NAME}-svc.${MDB_NS}.svc.cluster.local"
  "*.${MDB_RESOURCE_NAME}-svc.${MDB_NS}.svc.cluster.local"
)

kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF_MANIFEST
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${MDB_RESOURCE_NAME}-mongos-tls
  namespace: ${MDB_NS}
spec:
  secretName: ${MDB_TLS_CERT_SECRET_PREFIX}-${MDB_RESOURCE_NAME}-mongos-cert
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
  config_dns_names+=("${MDB_RESOURCE_NAME}-config-${member}")
  config_dns_names+=("${MDB_RESOURCE_NAME}-config-${member}.${MDB_RESOURCE_NAME}-cs.${MDB_NS}.svc.cluster.local")
done
config_dns_names+=(
  "${MDB_RESOURCE_NAME}-cs.${MDB_NS}.svc.cluster.local"
  "*.${MDB_RESOURCE_NAME}-cs.${MDB_NS}.svc.cluster.local"
)

kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF_MANIFEST
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${MDB_RESOURCE_NAME}-config-tls
  namespace: ${MDB_NS}
spec:
  secretName: ${MDB_TLS_CERT_SECRET_PREFIX}-${MDB_RESOURCE_NAME}-config-cert
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
# Note: The operator uses mdb-sh-sh as the headless service for all shards
for ((shard = 0; shard < MDB_SHARD_COUNT; shard++)); do
  shard_dns_names=()
  for ((member = 0; member < MDB_MONGODS_PER_SHARD; member++)); do
    shard_dns_names+=("${MDB_RESOURCE_NAME}-${shard}-${member}")
    # The operator uses mdb-sh-sh as the headless service for shards
    shard_dns_names+=("${MDB_RESOURCE_NAME}-${shard}-${member}.${MDB_RESOURCE_NAME}-sh.${MDB_NS}.svc.cluster.local")
  done
  shard_dns_names+=(
    "${MDB_RESOURCE_NAME}-sh.${MDB_NS}.svc.cluster.local"
    "*.${MDB_RESOURCE_NAME}-sh.${MDB_NS}.svc.cluster.local"
  )

  kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF_MANIFEST
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${MDB_RESOURCE_NAME}-${shard}-tls
  namespace: ${MDB_NS}
spec:
  secretName: ${MDB_TLS_CERT_SECRET_PREFIX}-${MDB_RESOURCE_NAME}-${shard}-cert
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

# Build DNS names for per-shard mongot services
search_dns_names=()
for ((shard = 0; shard < MDB_SHARD_COUNT; shard++)); do
  shard_name="${MDB_RESOURCE_NAME}-${shard}"
  search_dns_names+=("${MDB_RESOURCE_NAME}-mongot-${shard_name}-svc.${MDB_NS}.svc.cluster.local")
  search_dns_names+=("*.${MDB_RESOURCE_NAME}-mongot-${shard_name}-svc.${MDB_NS}.svc.cluster.local")
done
# Also add the main search service
search_dns_names+=("${MDB_RESOURCE_NAME}-search-svc.${MDB_NS}.svc.cluster.local")

# Create certificate for MongoDBSearch (mongot)
kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF_MANIFEST
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${MDB_RESOURCE_NAME}-search-tls
  namespace: ${MDB_NS}
spec:
  secretName: ${MDB_SEARCH_TLS_SECRET_NAME}
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
$(render_dns_list "${search_dns_names[@]}")
EOF_MANIFEST

# Wait for all certificates to be ready
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" wait --for=condition=Ready certificate "${MDB_RESOURCE_NAME}-mongos-tls" --timeout=300s
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" wait --for=condition=Ready certificate "${MDB_RESOURCE_NAME}-config-tls" --timeout=300s
for ((shard = 0; shard < MDB_SHARD_COUNT; shard++)); do
  kubectl --context "${K8S_CTX}" -n "${MDB_NS}" wait --for=condition=Ready certificate "${MDB_RESOURCE_NAME}-${shard}-tls" --timeout=300s
done
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" wait --for=condition=Ready certificate "${MDB_RESOURCE_NAME}-search-tls" --timeout=300s

echo "TLS certificates created for sharded cluster and MongoDBSearch"
