echo "Waiting for the AppDB MongoDB CR to adopt the StatefulSet and become Running..."
kubectl wait --for=jsonpath='{.status.phase}'=Running \
  mdb/"${APPDB_NAME}" \
  --context "${K8S_CTX}" -n "${MDB_NS}" --timeout=1200s

echo "Waiting for the primary Ops Manager to be Running against the external AppDB..."
kubectl wait --for=jsonpath='{.status.opsManager.phase}'=Running \
  om/"${PRIMARY_OM_NAME}" \
  --context "${K8S_CTX}" -n "${MDB_NS}" --timeout=1800s

echo "[ok] Forward migration complete: '${APPDB_NAME}' adopted, primary OM Running on external AppDB"
