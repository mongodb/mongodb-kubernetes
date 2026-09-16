# Create the external AppDB MongoDB CR (role: AppDB), named "<primary-om-name>-db". Because the
# primary OM's internal AppDB already owns a StatefulSet with that name, the CR cannot adopt it yet:
# it will report Pending on the adoption gate until the OM detaches (next step sets the ref).
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

echo "[ok] AppDB MongoDB resource '${APPDB_NAME}' created (awaiting adoption gate)"
