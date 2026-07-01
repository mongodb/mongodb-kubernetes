echo "Creating operator-managed multi-cluster MongoDB replica set..."
echo "  Members per cluster: ${MDB_MEMBERS_PER_CLUSTER}"

# On multi-cluster, the operator does NOT auto-wire mongod -> mongot: the
# MongoDBMultiCluster path has no search automation, and a multi-cluster
# MongoDBSearch (spec.clusters > 1) consumes the source as an external
# deployment. The search setParameters are therefore configured by hand on
# spec.additionalMongodConfig, which the operator applies to every mongod
# process across all member clusters.
#
# MongoDBMultiCluster has no per-cluster additionalMongodConfig field today, so
# mongotHost is a single value applied to every member. It points at cluster
# 0's Envoy proxy Service, which is reachable from every member cluster over
# the service mesh; mongots in other clusters are still deployed (one per
# cluster) but mongods route search traffic to cluster 0's Envoy.
kubectl apply --context "${K8S_CTX_0}" -n "${MDB_NS}" -f - <<EOF
apiVersion: mongodb.com/v1
kind: MongoDBMultiCluster
metadata:
  name: ${MDB_RESOURCE_NAME}
spec:
  type: ReplicaSet
  version: ${MDB_VERSION}
  duplicateServiceObjects: false
  opsManager:
    configMapRef:
      name: om-project
  credentials: om-credentials
  persistent: true
  security:
    certsSecretPrefix: ${MDB_TLS_CERT_SECRET_PREFIX}
    tls:
      ca: ${MDB_TLS_CA_CONFIGMAP}
    authentication:
      enabled: true
      modes: ["SCRAM"]
  additionalMongodConfig:
    setParameter:
      searchTLSMode: requireTLS
      useGrpcForSearch: true
      skipAuthenticationToMongot: false
      skipAuthenticationToSearchIndexManagementServer: false
      mongotHost: "${MDB_PROXY_HOST_0}"
      searchIndexManagementHostAndPort: "${MDB_PROXY_HOST_0}"
  clusterSpecList:
    - clusterName: ${K8S_CTX_0}
      members: ${MDB_MEMBERS_PER_CLUSTER}
    - clusterName: ${K8S_CTX_1}
      members: ${MDB_MEMBERS_PER_CLUSTER}
EOF

echo "[ok] MongoDBMultiCluster replica set resource created"
