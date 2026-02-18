#
# This uses the Enterprise MongoDB CRD to create a sharded cluster.
# The MongoDBSearch resource will treat this as an "external" source,
# meaning it will connect to it using the external source configuration
# rather than the mongodbResourceRef.
#
# In a real-world scenario, this would be an existing MongoDB sharded cluster
# running outside of Kubernetes (e.g., MongoDB Atlas, self-hosted, etc.)
#
# Note: TLS certificates must be created before running this script.
# See 06_0304_generate_tls_certificates.sh
#
# The search configuration (mongotHost, searchIndexManagementHostAndPort, etc.) is
# included from the start. The Envoy proxy services don't exist yet, but the
# service names are predictable. MongoDB will start with the search config, and
# when Envoy proxy is deployed later, the connections will work.

# Build shardOverrides configuration dynamically
# Each shard needs to point to its own Envoy proxy service
shard_overrides_yaml=""
for ((shard = 0; shard < MDB_SHARD_COUNT; shard++)); do
  shard_name="${MDB_EXTERNAL_CLUSTER_NAME}-${shard}"
  # Envoy proxy service name follows the pattern: <search-name>-mongot-<shard-name>-proxy-svc
  proxy_host="${MDB_SEARCH_RESOURCE_NAME}-mongot-${shard_name}-proxy-svc.${MDB_NS}.svc.cluster.local:${ENVOY_PROXY_PORT:-27029}"

  shard_overrides_yaml="${shard_overrides_yaml}
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

# For mongos, we use the first shard's Envoy proxy service as the search endpoint
first_shard_name="${MDB_EXTERNAL_CLUSTER_NAME}-0"
mongos_proxy_host="${MDB_SEARCH_RESOURCE_NAME}-mongot-${first_shard_name}-proxy-svc.${MDB_NS}.svc.cluster.local:${ENVOY_PROXY_PORT:-27029}"

# Create the MongoDB Sharded Cluster WITH search configuration
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
  # Configure mongos with search parameters to route search queries via Envoy
  mongos:
    additionalMongodConfig:
      setParameter:
        mongotHost: ${mongos_proxy_host}
        searchIndexManagementHostAndPort: ${mongos_proxy_host}
        skipAuthenticationToSearchIndexManagementServer: false
        skipAuthenticationToMongot: false
        searchTLSMode: requireTLS
        useGrpcForSearch: true
  # Configure each shard with its own Envoy proxy endpoint using shardOverrides
  shardOverrides:${shard_overrides_yaml}
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

echo "MongoDB Sharded Cluster '${MDB_EXTERNAL_CLUSTER_NAME}' created with search configuration"
echo "  - Each shard's mongod points to its Envoy proxy service"
echo "  - Mongos points to first shard's Envoy proxy service"
echo "  - Traffic flow: mongod -> Envoy proxy -> mongot"
