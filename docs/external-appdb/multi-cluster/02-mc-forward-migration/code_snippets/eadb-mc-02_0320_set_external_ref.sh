# Flip the primary OM to the external AppDB: add spec.externalApplicationDatabaseRef pointing at the
# MongoDBMultiCluster CR. The operator detaches its internal AppDB StatefulSets (drops ownership and
# marks them migration-ready) so the MongoDBMultiCluster CR can adopt them per member cluster. The
# computed connection string is unchanged (same hosts), so the OM pods do not roll.
kubectl patch om "${PRIMARY_OM_NAME}" \
  --context "${K8S_CTX_0}" -n "${MDB_NS}" \
  --type merge \
  -p "{\"spec\":{\"externalApplicationDatabaseRef\":{\"name\":\"${APPDB_NAME}\",\"kind\":\"MongoDBMultiCluster\"}}}"

echo "[ok] Primary OM '${PRIMARY_OM_NAME}' now references external AppDB '${APPDB_NAME}' (kind MongoDBMultiCluster)"
