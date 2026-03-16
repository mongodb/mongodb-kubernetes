#!/usr/bin/env bash
# Create the simulated external MongoDB sharded cluster
# The cluster is created WITH search parameters pre-configured to point to the
# operator-managed Envoy proxy endpoints created when MongoDBSearch is deployed.

echo "Creating simulated external MongoDB sharded cluster..."
echo "  Shards: ${MDB_SHARD_COUNT}"
echo "  Members per shard: ${MDB_MONGODS_PER_SHARD}"
echo "  mongos count: ${MDB_MONGOS_COUNT}"
echo "  Config servers: ${MDB_CONFIG_SERVER_COUNT}"

# Build shardOverrides with search parameters for each shard
shard_overrides=""
for shard_name in ${MDB_EXTERNAL_SHARD_NAMES}; do
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

# mongos search parameters (uses first shard's proxy as entry point)
read -r first_shard _ <<< "${MDB_EXTERNAL_SHARD_NAMES}"
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
  shardOverrides:${shard_overrides}
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
