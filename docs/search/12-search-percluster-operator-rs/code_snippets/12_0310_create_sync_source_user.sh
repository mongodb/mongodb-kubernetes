echo "Creating the search-sync-source MongoDBUser on the source MongoDBMultiCluster..."

# MongoDBUser is managed by the CENTRAL hub-and-spoke operator (ra-02) that owns the
# source MongoDBMultiCluster -- applied once, only on the central cluster.
kubectl apply --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" -f - <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: ${SEARCH_RESOURCE_NAME}-${SEARCH_SYNC_USER_NAME}-password
type: Opaque
stringData:
  password: ${SEARCH_SYNC_USER_PASSWORD}
---
apiVersion: mongodb.com/v1
kind: MongoDBUser
metadata:
  name: ${RS_RESOURCE_NAME}-${SEARCH_SYNC_USER_NAME}
spec:
  username: ${SEARCH_SYNC_USER_NAME}
  db: admin
  mongodbResourceRef:
    name: ${RS_RESOURCE_NAME}
  passwordSecretKeyRef:
    name: ${SEARCH_RESOURCE_NAME}-${SEARCH_SYNC_USER_NAME}-password
    key: password
  roles:
  - name: searchCoordinator
    db: admin
EOF

kubectl --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" wait \
  --for=jsonpath='{.status.phase}'=Updated \
  "mongodbuser/${RS_RESOURCE_NAME}-${SEARCH_SYNC_USER_NAME}" \
  --timeout=300s

echo "[ok] search-sync-source user created"

# Every per-cluster Search operator reads this password locally -- there is no
# cross-cluster secret replication in this model, so the customer copies it.
echo "Replicating the sync-source password secret to every member cluster..."
for ctx in "${K8S_CLUSTER_1_CONTEXT_NAME}" "${K8S_CLUSTER_2_CONTEXT_NAME}"; do
  kubectl get secret "${SEARCH_RESOURCE_NAME}-${SEARCH_SYNC_USER_NAME}-password" \
    -n "${MDB_NAMESPACE}" --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -o json \
    | jq 'del(.metadata.uid, .metadata.resourceVersion, .metadata.creationTimestamp, .metadata.selfLink, .metadata.managedFields, .metadata.ownerReferences, .status)' \
    | kubectl apply --context "${ctx}" -n "${MDB_NAMESPACE}" -f -

  echo "  [ok] password secret replicated to ${ctx}"
done
