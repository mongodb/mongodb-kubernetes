# Flip the primary OM to the external AppDB: add spec.externalApplicationDatabaseRef pointing at the
# MongoDB CR. The operator detaches its internal AppDB StatefulSet (drops its OwnerReference and marks
# it migration-ready) so the MongoDB CR can adopt it. The computed connection string is unchanged
# (same hosts), so the OM pods do not roll.
kubectl patch om "${PRIMARY_OM_NAME}" \
  --context "${K8S_CTX}" -n "${MDB_NS}" \
  --type merge \
  -p "{\"spec\":{\"externalApplicationDatabaseRef\":{\"name\":\"${APPDB_NAME}\",\"kind\":\"MongoDB\"}}}"

echo "[ok] Primary OM '${PRIMARY_OM_NAME}' now references external AppDB '${APPDB_NAME}'"
