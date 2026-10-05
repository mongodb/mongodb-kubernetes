echo "Verifying the AppDB CR is blocked on the adoption gate (StatefulSets still owned by the OM's internal AppDB)..."

# The MongoDBMultiCluster CR must NOT adopt the StatefulSets while they are still owned by the Ops Manager.
kubectl wait --for=jsonpath='{.status.phase}'=Pending \
  mdbmc/"${APPDB_NAME}" \
  --context "${K8S_CTX_0}" -n "${MDB_NS}" --timeout=300s

echo "AppDB CR status message:"
kubectl get mdbmc "${APPDB_NAME}" --context "${K8S_CTX_0}" -n "${MDB_NS}" \
  -o jsonpath='{.status.message}{"\n"}'

echo "[ok] AppDB '${APPDB_NAME}' is Pending on the adoption gate (as expected before the switch)"
