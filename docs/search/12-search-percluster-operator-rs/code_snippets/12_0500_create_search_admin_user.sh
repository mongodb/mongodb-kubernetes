: "${K8S_CLUSTER_0_CONTEXT_NAME:?not set -- source the env files first (see README, Environment section)}"
: "${MDB_NAMESPACE:?not set -- source the env files first (see README, Environment section)}"
: "${RS_RESOURCE_NAME:?not set -- source the env files first (see README, Environment section)}"
: "${SEARCH_ADMIN_USER_NAME:?not set -- source the env files first (see README, Environment section)}"
: "${SEARCH_ADMIN_USER_PASSWORD:?not set -- source the env files first (see README, Environment section)}"

echo "Creating an admin MongoDBUser for loading data and running search queries..."

# Like the sync-source user (12_0310), MongoDBUser is managed by the CENTRAL
# hub-and-spoke operator that owns the source MongoDBMultiCluster -- applied once,
# only on the central cluster. Nothing search-specific here: it's a plain
# readWriteAnyDatabase user we use to insert sample data and run queries.
kubectl apply --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" -f - <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: ${RS_RESOURCE_NAME}-${SEARCH_ADMIN_USER_NAME}-password
type: Opaque
stringData:
  password: ${SEARCH_ADMIN_USER_PASSWORD}
---
apiVersion: mongodb.com/v1
kind: MongoDBUser
metadata:
  name: ${RS_RESOURCE_NAME}-${SEARCH_ADMIN_USER_NAME}
spec:
  username: ${SEARCH_ADMIN_USER_NAME}
  db: admin
  mongodbResourceRef:
    name: ${RS_RESOURCE_NAME}
  passwordSecretKeyRef:
    name: ${RS_RESOURCE_NAME}-${SEARCH_ADMIN_USER_NAME}-password
    key: password
  roles:
  - name: readWriteAnyDatabase
    db: admin
  # clusterMonitor lets the verification steps read each member's runtime
  # mongotHost (getParameter) -- the locality proof in the final step.
  - name: clusterMonitor
    db: admin
EOF

kubectl --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" wait \
  --for=jsonpath='{.status.phase}'=Updated \
  "mongodbuser/${RS_RESOURCE_NAME}-${SEARCH_ADMIN_USER_NAME}" \
  --timeout=300s

echo "[ok] ${SEARCH_ADMIN_USER_NAME} user created"
