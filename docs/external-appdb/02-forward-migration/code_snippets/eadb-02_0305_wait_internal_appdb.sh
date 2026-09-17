echo "Waiting for the primary Ops Manager's internal AppDB to become Running..."
kubectl wait --for=jsonpath='{.status.applicationDatabase.phase}'=Running \
  om/"${PRIMARY_OM_NAME}" \
  --context "${K8S_CTX}" -n "${MDB_NS}" --timeout=1200s

echo "Waiting for the primary Ops Manager to become Running (pulling the OM image can take a while)..."
kubectl wait --for=jsonpath='{.status.opsManager.phase}'=Running \
  om/"${PRIMARY_OM_NAME}" \
  --context "${K8S_CTX}" -n "${MDB_NS}" --timeout=1800s

echo "[ok] Primary OM is Running with an internal AppDB (StatefulSet '${APPDB_NAME}' owned by the OM)"
