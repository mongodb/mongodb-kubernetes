# Create MongoDBSearch resource with External Sharded MongoDB Source
#
# This demonstrates how to configure MongoDBSearch to connect to an external
# sharded MongoDB cluster that is NOT managed by the Kubernetes operator.
#
# Key configuration:
# - spec.source.external.sharded: Contains the external sharded cluster configuration
#   - router.hosts: List of mongos router endpoints
#   - shards: List of shard configurations with shardName and hosts
# - spec.source.external.tls: TLS configuration for connecting to the external cluster
# - spec.source.username/passwordSecretRef: Credentials for the search-sync-source user
#
# The MongoDBSearch operator will:
# 1. Deploy one mongot StatefulSet per shard
# 2. Configure each mongot to sync from its corresponding shard
# 3. Use the mongos router for search index management

# Build the shards configuration dynamically
shards_yaml=""
for ((shard = 0; shard < MDB_SHARD_COUNT; shard++)); do
  shard_name="${MDB_EXTERNAL_CLUSTER_NAME}-${shard}"

  # Build hosts list for this shard
  hosts_yaml=""
  for ((member = 0; member < MDB_MONGODS_PER_SHARD; member++)); do
    host="${MDB_EXTERNAL_CLUSTER_NAME}-${shard}-${member}.${MDB_EXTERNAL_CLUSTER_NAME}-sh.${MDB_NS}.svc.cluster.local:27017"
    hosts_yaml="${hosts_yaml}
            - ${host}"
  done

  shards_yaml="${shards_yaml}
        - shardName: ${shard_name}
          hosts:${hosts_yaml}"
done

# Build mongos router hosts
router_hosts_yaml=""
for ((i = 0; i < MDB_MONGOS_COUNT; i++)); do
  host="${MDB_EXTERNAL_CLUSTER_NAME}-mongos-${i}.${MDB_EXTERNAL_CLUSTER_NAME}-svc.${MDB_NS}.svc.cluster.local:27017"
  router_hosts_yaml="${router_hosts_yaml}
          - ${host}"
done

# Build the lb endpoints configuration for Envoy proxy
# These endpoints tell the operator where mongod/mongos should connect to reach mongot
lb_endpoints_yaml=""
for ((shard = 0; shard < MDB_SHARD_COUNT; shard++)); do
  shard_name="${MDB_EXTERNAL_CLUSTER_NAME}-${shard}"
  # Envoy proxy service endpoint (port 27029)
  endpoint="${MDB_SEARCH_RESOURCE_NAME}-mongot-${shard_name}-proxy-svc.${MDB_NS}.svc.cluster.local:${ENVOY_PROXY_PORT:-27029}"
  lb_endpoints_yaml="${lb_endpoints_yaml}
          - shardName: ${shard_name}
            endpoint: ${endpoint}"
done

kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: mongodb.com/v1
kind: MongoDBSearch
metadata:
  name: ${MDB_SEARCH_RESOURCE_NAME}
spec:
  logLevel: DEBUG
  source:
    username: search-sync-source
    passwordSecretRef:
      name: ${MDB_SEARCH_RESOURCE_NAME}-search-sync-source-password
      key: password
    external:
      sharded:
        router:
          hosts:${router_hosts_yaml}
        shards:${shards_yaml}
      tls:
        ca:
          name: ${MDB_TLS_CA_SECRET_NAME}
  security:
    tls:
      certificateKeySecretRef:
        name: ${MDB_SEARCH_TLS_SECRET_NAME}
  lb:
    mode: External
    external:
      sharded:
        endpoints:${lb_endpoints_yaml}
  resourceRequirements:
    limits:
      cpu: "2"
      memory: 3Gi
    requests:
      cpu: "1"
      memory: 2Gi
EOF

echo "MongoDBSearch resource '${MDB_SEARCH_RESOURCE_NAME}' created with external sharded source"
