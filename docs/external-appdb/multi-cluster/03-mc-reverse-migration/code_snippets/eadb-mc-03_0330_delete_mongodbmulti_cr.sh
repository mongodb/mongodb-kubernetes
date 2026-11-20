# The handover is complete and the Ops Manager owns the AppDB StatefulSets. The now-released
# MongoDBMultiCluster CR is a leftover and can be deleted safely (its StatefulSets are no longer owned
# by it — deleting the CR does not delete them).
kubectl delete mdbmc "${APPDB_NAME}" \
  --context "${K8S_CTX_0}" -n "${MDB_NS}" --wait=true --timeout=300s

echo "[ok] Released MongoDBMultiCluster CR '${APPDB_NAME}' deleted"
