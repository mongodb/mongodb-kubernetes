# Create a MongoDB Sharded Cluster to simulate an external MongoDB deployment
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

# Create the MongoDB Sharded Cluster
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

echo "MongoDB Sharded Cluster '${MDB_EXTERNAL_CLUSTER_NAME}' created"
