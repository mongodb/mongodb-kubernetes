# Create MongoDBSearch resource with External LB configuration for sharded cluster
#
# For sharded clusters with external L7 LB (Envoy), we configure per-shard endpoints
# that point to the Envoy proxy services. Envoy uses SNI-based routing to direct
# traffic to the appropriate per-shard mongot service.
#
# Traffic flow:
#   mongod -> Envoy proxy (port 27029) -> mongot (port 27028)
#
# The endpoints point to the per-shard Envoy proxy services:
# - <search-name>-mongot-<shard-name>-proxy-svc.<namespace>.svc.cluster.local:27029
#
# Envoy routes based on SNI to the actual mongot services:
# - <search-name>-mongot-<shard-name>-svc.<namespace>.svc.cluster.local:27028
#
# When MDB_MONGOT_REPLICAS > 1, multiple mongot pods are deployed per shard.
# Envoy load balances across all mongot pods for each shard.
#
# TLS Configuration:
# - certificateKeySecretRef: Server certificate for mongot TLS
# - ca: CA certificate for mTLS (validates client certificates from Envoy)

# Build the endpoints array dynamically based on shard count
# Endpoints point to Envoy proxy services (port 27029)
endpoints_yaml=""
for i in $(seq 0 $((MDB_SHARD_COUNT - 1))); do
  shard_name="${MDB_RESOURCE_NAME}-${i}"
  # Use Envoy proxy service endpoint (port 27029)
  endpoint="${MDB_RESOURCE_NAME}-mongot-${shard_name}-proxy-svc.${MDB_NS}.svc.cluster.local:27029"
  endpoints_yaml="${endpoints_yaml}
          - shardName: ${shard_name}
            endpoint: ${endpoint}"
done

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
      certificateKeySecretRef:
        name: ${MDB_SEARCH_TLS_SECRET_NAME}
  lb:
    mode: External
    external:
      sharded:
        endpoints:${endpoints_yaml}
  resourceRequirements:
    limits:
      cpu: "2"
      memory: 3Gi
    requests:
      cpu: "1"
      memory: 2Gi
EOF
