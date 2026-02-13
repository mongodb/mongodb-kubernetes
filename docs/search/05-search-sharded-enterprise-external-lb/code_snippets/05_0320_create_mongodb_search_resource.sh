# Create MongoDBSearch resource with External LB configuration for sharded cluster
#
# For sharded clusters with external L7 LB (Envoy), we configure an endpoint template
# using the {shardName} placeholder. The operator substitutes this with actual shard names.
#
# Traffic flow:
#   mongod -> Envoy proxy (port 27029) -> mongot (port 27028)
#
# Endpoint template format:
#   <search-name>-mongot-{shardName}-proxy-svc.<namespace>.svc.cluster.local:27029
#
# The operator expands {shardName} to actual shard names (e.g., mdb-sh-0, mdb-sh-1)
#
# Envoy routes based on SNI to the actual mongot services:
# - <search-name>-mongot-<shard-name>-svc.<namespace>.svc.cluster.local:27028
#
# When MDB_MONGOT_REPLICAS > 1, multiple mongot pods are deployed per shard.
# Envoy load balances across all mongot pods for each shard.
#
# TLS Configuration (Per-Shard):
# - certsSecretPrefix: Prefix for per-shard TLS secrets
#   Each shard gets its own certificate: {prefix}-{shardName}-search-cert
#   e.g., certs-mdb-sh-0-search-cert, certs-mdb-sh-1-search-cert

# Build the source section with mongodbResourceRef and optional replicas
# Note: JSON field name is "mongodbResourceRef" (lowercase 'db')
source_yaml="
  source:
    mongodbResourceRef:
      name: ${MDB_RESOURCE_NAME}"

if [[ "${MDB_MONGOT_REPLICAS:-1}" -gt 1 ]]; then
  source_yaml="${source_yaml}
    replicas: ${MDB_MONGOT_REPLICAS}"
  echo "Configuring ${MDB_MONGOT_REPLICAS} mongot replicas per shard"
fi

kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: mongodb.com/v1
kind: MongoDBSearch
metadata:
  name: ${MDB_RESOURCE_NAME}
spec:
  logLevel: DEBUG${source_yaml}
  security:
    tls:
      # Per-shard TLS: each shard gets its own certificate
      # Secrets are named: {prefix}-{shardName}-search-cert
      # e.g., certs-mdb-sh-0-search-cert, certs-mdb-sh-1-search-cert
      certsSecretPrefix: ${MDB_SEARCH_TLS_CERT_PREFIX}
  lb:
    mode: External
    external:
      # Endpoint template with {shardName} placeholder
      # The operator substitutes {shardName} with actual shard names
      endpoint: "${MDB_RESOURCE_NAME}-mongot-{shardName}-proxy-svc.${MDB_NS}.svc.cluster.local:${ENVOY_PROXY_PORT}"
  resourceRequirements:
    limits:
      cpu: "2"
      memory: 3Gi
    requests:
      cpu: "1"
      memory: 2Gi
EOF
