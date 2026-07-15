echo "Creating the single-cluster, operator-managed sharded MongoDB source (cluster 0)..."
echo "  Shards: ${MDB_SHARD_COUNT}, mongods/shard: ${MDB_MONGODS_PER_SHARD}, mongos: ${MDB_MONGOS_COUNT}, config servers: ${MDB_CONFIG_SERVER_COUNT}"

# This is deliberately a single-cluster MongoDB (type: ShardedCluster, no
# topology: MultiCluster) -- it is NOT spread across the same clusters as
# MongoDBSearch. See the README for why this scenario doesn't co-locate
# source and search the way scenario 12 (replica-set case) does.
#
# mongotHost/searchIndexManagementHostAndPort are deliberately absent from
# additionalMongodConfig: they're set later directly on the OM Automation
# Config (the "route the source" step), so operator reconciles never clobber
# that patch.
kubectl apply --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" -f - <<EOF
apiVersion: mongodb.com/v1
kind: MongoDB
metadata:
  name: ${MDB_RESOURCE_NAME}
spec:
  type: ShardedCluster
  shardCount: ${MDB_SHARD_COUNT}
  mongodsPerShardCount: ${MDB_MONGODS_PER_SHARD}
  mongosCount: ${MDB_MONGOS_COUNT}
  configServerCount: ${MDB_CONFIG_SERVER_COUNT}
  version: ${MDB_VERSION}
  opsManager:
    configMapRef:
      name: mdb-org-project-config
  credentials: mdb-org-owner-credentials
  security:
    certsSecretPrefix: ${MDB_TLS_CERT_SECRET_PREFIX}
    tls:
      enabled: true
      ca: ${MDBS_TLS_CA_CONFIGMAP}
    authentication:
      enabled: true
      ignoreUnknownUsers: true
      modes:
        - SCRAM
  mongos:
    additionalMongodConfig:
      setParameter:
        skipAuthenticationToSearchIndexManagementServer: false
        skipAuthenticationToMongot: false
        searchTLSMode: requireTLS
        useGrpcForSearch: true
  shard:
    additionalMongodConfig:
      setParameter:
        skipAuthenticationToSearchIndexManagementServer: false
        skipAuthenticationToMongot: false
        searchTLSMode: requireTLS
        useGrpcForSearch: true
  # Config servers get no search setParameters -- they never talk to mongot.
  agent:
    logLevel: DEBUG
  persistent: true
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

echo "[ok] MongoDB sharded source resource created"
