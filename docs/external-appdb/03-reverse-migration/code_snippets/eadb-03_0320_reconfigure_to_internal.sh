# Reverse migration (graceful): remove spec.externalApplicationDatabaseRef and add
# spec.applicationDatabase. The operator asks the MongoDB CR to release the StatefulSet, then adopts
# it back as an internally-managed AppDB. The MongoDB CR reports Pending ("unmanaged"/released) and
# can be deleted once the handover completes. The connection string is unchanged (same hosts).
kubectl patch om "${PRIMARY_OM_NAME}" \
  --context "${K8S_CTX}" -n "${MDB_NS}" \
  --type merge \
  -p "{\"spec\":{\"externalApplicationDatabaseRef\":null,\"applicationDatabase\":{\"members\":3,\"version\":\"${APPDB_VERSION}\"}}}"

echo "[ok] Primary OM '${PRIMARY_OM_NAME}' reconfigured to an internal AppDB"
