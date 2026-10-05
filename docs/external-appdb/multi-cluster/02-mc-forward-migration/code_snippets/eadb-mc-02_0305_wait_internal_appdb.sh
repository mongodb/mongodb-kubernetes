echo "Waiting for the internal multi-cluster AppDB to become Running..."
kubectl wait --for=jsonpath='{.status.applicationDatabase.phase}'=Running \
  om/"${PRIMARY_OM_NAME}" \
  --context "${K8S_CTX_0}" -n "${MDB_NS}" \
  --timeout=1200s

echo "Waiting for the primary Ops Manager to become Running..."
kubectl wait --for=jsonpath='{.status.opsManager.phase}'=Running \
  om/"${PRIMARY_OM_NAME}" \
  --context "${K8S_CTX_0}" -n "${MDB_NS}" \
  --timeout=1800s

echo "[ok] Primary Ops Manager '${PRIMARY_OM_NAME}' is Running with its internal multi-cluster AppDB"
