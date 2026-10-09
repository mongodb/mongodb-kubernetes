echo "Waiting for the MongoDBMultiCluster CR to be released (it becomes Pending / other owner)..."
kubectl wait --for=jsonpath='{.status.phase}'=Pending \
  mdbmc/"${APPDB_NAME}" \
  --context "${K8S_CTX_0}" -n "${MDB_NS}" --timeout=300s
kubectl get mdbmc "${APPDB_NAME}" --context "${K8S_CTX_0}" -n "${MDB_NS}" \
  -o jsonpath='{.status.message}{"\n"}'

echo "Waiting for the primary OM to manage the internal multi-cluster AppDB again (Running)..."
kubectl wait --for=jsonpath='{.status.applicationDatabase.phase}'=Running \
  om/"${PRIMARY_OM_NAME}" \
  --context "${K8S_CTX_0}" -n "${MDB_NS}" --timeout=1200s
kubectl wait --for=jsonpath='{.status.opsManager.phase}'=Running \
  om/"${PRIMARY_OM_NAME}" \
  --context "${K8S_CTX_0}" -n "${MDB_NS}" --timeout=1800s

echo "[ok] Ops Manager re-adopted the AppDB StatefulSets as an internal multi-cluster AppDB"
