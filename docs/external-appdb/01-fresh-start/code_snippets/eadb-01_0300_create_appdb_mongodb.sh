# The external AppDB: a normal MongoDB replica set with role: AppDB, managed by the management OM.
# Its name MUST be "<primary-om-name>-db" so the primary OM's externalApplicationDatabaseRef resolves it.
kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: mongodb.com/v1
kind: MongoDB
metadata:
  name: ${APPDB_NAME}
spec:
  members: 3
  version: ${APPDB_VERSION}
  type: ReplicaSet
  role: AppDB
  opsManager:
    configMapRef:
      name: ${APPDB_PROJECT_CONFIGMAP}
  credentials: ${MANAGEMENT_OM_ADMIN_KEY_SECRET}
  persistent: true
  security:
    authentication:
      enabled: true
      modes: ["SCRAM"]
      ignoreUnknownUsers: true
EOF

echo "[ok] AppDB MongoDB resource '${APPDB_NAME}' (role: AppDB) created"
