echo "Creating the search-sync-source user on the sharded source MongoDB (cluster 0)..."

kubectl apply --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" -f - <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: ${MDBS_RESOURCE_NAME}-search-sync-source-password
type: Opaque
stringData:
  password: ${MDBS_SEARCH_SYNC_USER_PASSWORD}
---
apiVersion: mongodb.com/v1
kind: MongoDBUser
metadata:
  name: ${MDB_RESOURCE_NAME}-search-sync-source
spec:
  username: search-sync-source
  db: admin
  mongodbResourceRef:
    name: ${MDB_RESOURCE_NAME}
  passwordSecretKeyRef:
    name: ${MDBS_RESOURCE_NAME}-search-sync-source-password
    key: password
  roles:
  - name: searchCoordinator
    db: admin
EOF

echo "Waiting for search sync source user to be ready..."
kubectl wait --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" \
  --for=jsonpath='{.status.phase}'=Updated \
  "mongodbuser/${MDB_RESOURCE_NAME}-search-sync-source" \
  --timeout=300s

echo "[ok] search-sync-source user created"
