#!/usr/bin/env bash
# Create the simulated external MongoDB sharded cluster
#
# This deploys a MongoDB Enterprise sharded cluster to simulate an external MongoDB.
# In a real scenario, your external cluster would already be running somewhere else.
#
# IMPORTANT: The cluster is created WITH search parameters already configured!
# Each shard's mongod is configured to point to the operator-managed Envoy proxy
# endpoints that will be created when MongoDBSearch is deployed.
#
# Proxy endpoint format:
#   {search-name}-search-0-{shard-name}-proxy-svc.{namespace}.svc.cluster.local:27029

echo "Creating simulated external MongoDB sharded cluster..."
echo "  Shards: ${MDB_SHARD_COUNT}"
echo "  Members per shard: ${MDB_MONGODS_PER_SHARD}"
echo "  mongos count: ${MDB_MONGOS_COUNT}"
echo "  Config servers: ${MDB_CONFIG_SERVER_COUNT}"

# Build shardOverrides section for search parameters
# Each shard points to its corresponding operator-managed Envoy proxy Service
shard_overrides=""
for ((shard = 0; shard < MDB_SHARD_COUNT; shard++)); do
  shard_name="${MDB_EXTERNAL_CLUSTER_NAME}-${shard}"
  # This is the endpoint format the operator will create for managed LB
  proxy_host="${MDB_SEARCH_RESOURCE_NAME}-search-0-${shard_name}-proxy-svc.${MDB_NS}.svc.cluster.local:${ENVOY_PROXY_PORT:-27029}"
  
  shard_overrides="${shard_overrides}
    - shardNames:
        - ${shard_name}
      additionalMongodConfig:
        setParameter:
          mongotHost: ${proxy_host}
          searchIndexManagementHostAndPort: ${proxy_host}
          skipAuthenticationToSearchIndexManagementServer: false
          skipAuthenticationToMongot: false
          searchTLSMode: requireTLS
          useGrpcForSearch: true"
done

# Build mongos search parameters (uses first shard's proxy as entry point)
first_shard="${MDB_EXTERNAL_CLUSTER_NAME}-0"
mongos_proxy_host="${MDB_SEARCH_RESOURCE_NAME}-search-0-${first_shard}-proxy-svc.${MDB_NS}.svc.cluster.local:${ENVOY_PROXY_PORT:-27029}"

kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: mongodb.com/v1
kind: MongoDB
metadata:
  name: ${MDB_EXTERNAL_CLUSTER_NAME}
spec:
  type: ShardedCluster
  shardCount: ${MDB_SHARD_COUNT}
  mongodsPerShardCount: ${MDB_MONGODS_PER_SHARD}
  mongosCount: ${MDB_MONGOS_COUNT}
  configServerCount: ${MDB_CONFIG_SERVER_COUNT}
  version: ${MDB_VERSION}
  opsManager:
    configMapRef:
      name: om-project
  credentials: om-credentials
  security:
    certsSecretPrefix: ${MDB_TLS_CERT_SECRET_PREFIX}
    tls:
      enabled: true
      ca: ${MDB_TLS_CA_CONFIGMAP}
    authentication:
      enabled: true
      ignoreUnknownUsers: true
      modes:
        - SCRAM
  agent:
    logLevel: DEBUG
  persistent: true
  # Per-shard search parameters (pointing to operator-managed Envoy proxy)
  shardOverrides:${shard_overrides}
  # mongos search parameters
  mongos:
    additionalMongodConfig:
      setParameter:
        mongotHost: ${mongos_proxy_host}
        searchIndexManagementHostAndPort: ${mongos_proxy_host}
        skipAuthenticationToSearchIndexManagementServer: false
        skipAuthenticationToMongot: false
        searchTLSMode: requireTLS
        useGrpcForSearch: true
  podSpec:
    podTemplate:
      spec:
        containers:
          - name: mongodb-enterprise-database
            resources:
              limits:
                cpu: "1"
                memory: 1Gi
              requests:
                cpu: "0.5"
                memory: 512Mi
EOF

echo "✓ MongoDB sharded cluster resource created"
echo ""
echo "Note: mongod search parameters are pre-configured to point to:"
for ((shard = 0; shard < MDB_SHARD_COUNT; shard++)); do
  shard_name="${MDB_EXTERNAL_CLUSTER_NAME}-${shard}"
  echo "  - Shard ${shard_name}: ${MDB_SEARCH_RESOURCE_NAME}-search-0-${shard_name}-proxy-svc:${ENVOY_PROXY_PORT:-27029}"
done

