# The handover is complete and the Ops Manager owns the AppDB StatefulSet. The now-released MongoDB
# CR is a leftover and can be deleted safely (its StatefulSet is no longer owned by it).
kubectl delete mdb "${APPDB_NAME}" \
  --context "${K8S_CTX}" -n "${MDB_NS}" --wait=true --timeout=300s

echo "[ok] Released MongoDB CR '${APPDB_NAME}' deleted"
