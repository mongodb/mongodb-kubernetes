# The external AppDB: a MongoDBMultiCluster with role: AppDB, managed by the management OM and spread
# across the two member clusters. Its name MUST be "<primary-om-name>-db" so the primary OM's
# externalApplicationDatabaseRef resolves it. TLS is enabled (multi-cluster AppDB members talk across
# clusters), using the CA ConfigMap and the certsSecretPrefix configured earlier. The CR object is
# applied on the central cluster; the operator fans the members out to the member clusters.
kubectl apply --context "${K8S_CTX_0}" -n "${MDB_NS}" -f - <<EOF
apiVersion: mongodb.com/v1
kind: MongoDBMultiCluster
metadata:
  name: ${APPDB_NAME}
spec:
  version: ${APPDB_VERSION}
  type: ReplicaSet
  role: AppDB
  persistent: true
  credentials: ${MANAGEMENT_OM_ADMIN_KEY_SECRET}
  opsManager:
    configMapRef:
      name: ${APPDB_PROJECT_CONFIGMAP}
  security:
    certsSecretPrefix: ${APPDB_CERT_PREFIX}
    tls:
      enabled: true
      ca: ${APPDB_CA_CONFIGMAP}
  clusterSpecList:
    - clusterName: ${K8S_CTX_1}
      members: ${APPDB_MEMBERS_C1}
    - clusterName: ${K8S_CTX_2}
      members: ${APPDB_MEMBERS_C2}
EOF

echo "[ok] AppDB MongoDBMultiCluster '${APPDB_NAME}' (role: AppDB) created across ${K8S_CTX_1}, ${K8S_CTX_2}"
