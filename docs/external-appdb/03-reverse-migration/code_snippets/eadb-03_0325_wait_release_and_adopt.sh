echo "Waiting for the MongoDB CR to be released (it becomes Pending / unmanaged)..."
kubectl wait --for=jsonpath='{.status.phase}'=Pending \
  mdb/"${APPDB_NAME}" \
  --context "${K8S_CTX}" -n "${MDB_NS}" --timeout=300s
kubectl get mdb "${APPDB_NAME}" --context "${K8S_CTX}" -n "${MDB_NS}" \
  -o jsonpath='{.status.message}{"\n"}'

echo "Waiting for the primary OM to manage the internal AppDB again (Running)..."
kubectl wait --for=jsonpath='{.status.applicationDatabase.phase}'=Running \
  om/"${PRIMARY_OM_NAME}" \
  --context "${K8S_CTX}" -n "${MDB_NS}" --timeout=1200s
kubectl wait --for=jsonpath='{.status.opsManager.phase}'=Running \
  om/"${PRIMARY_OM_NAME}" \
  --context "${K8S_CTX}" -n "${MDB_NS}" --timeout=1800s

echo "[ok] Ops Manager re-adopted the AppDB StatefulSet as an internal AppDB"
