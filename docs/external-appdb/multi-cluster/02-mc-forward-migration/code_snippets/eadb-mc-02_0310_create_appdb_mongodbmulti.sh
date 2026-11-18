# Create the external AppDB MongoDBMultiCluster (role: AppDB), named "<primary-om-name>-db", matching
# the internal AppDB's topology (MultiCluster), member layout and TLS. Because the primary OM's
# internal AppDB already owns StatefulSets with that name, the CR cannot adopt them yet: it reports
# Pending on the adoption gate until the OM detaches (next step sets the ref).
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

echo "[ok] AppDB MongoDBMultiCluster '${APPDB_NAME}' created (awaiting adoption gate)"
