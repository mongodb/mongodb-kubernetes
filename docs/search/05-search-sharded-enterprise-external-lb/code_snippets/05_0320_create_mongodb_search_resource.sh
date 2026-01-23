# Create MongoDBSearch resource with External LB configuration for sharded cluster
#
# For sharded clusters with external L7 LB, we configure per-shard endpoints.
# In this PoC, we use the internal K8s service URLs as the "external" endpoints
# since we're not deploying a real L7 load balancer.
#
# The endpoints point to the per-shard mongot services that the operator creates:
# - <search-name>-mongot-<shard-name>-svc.<namespace>.svc.cluster.local:27028
#
# When MDB_MONGOT_REPLICAS > 1, multiple mongot pods are deployed per shard.
# The external LB endpoint for each shard should distribute traffic across
# all mongot pods for that shard.
#
# TLS Configuration:
# - certificateKeySecretRef: Server certificate for mongot TLS
# - ca: CA certificate for mTLS (validates client certificates from mongod)

# Build the endpoints array dynamically based on shard count
endpoints_yaml=""
for i in $(seq 0 $((MDB_SHARD_COUNT - 1))); do
  shard_name="${MDB_RESOURCE_NAME}-${i}"
  endpoint="${MDB_RESOURCE_NAME}-mongot-${shard_name}-svc.${MDB_NS}.svc.cluster.local:27028"
  endpoints_yaml="${endpoints_yaml}
          - shardName: ${shard_name}
            endpoint: ${endpoint}"
done

# Build the source section with replicas if MDB_MONGOT_REPLICAS > 1
source_yaml=""
if [[ "${MDB_MONGOT_REPLICAS:-1}" -gt 1 ]]; then
  source_yaml="
  source:
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
