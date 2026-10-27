echo "Waiting for the primary Ops Manager to become Running (pulling the OM image can take a while)..."
kubectl wait --for=jsonpath='{.status.opsManager.phase}'=Running \
  om/"${PRIMARY_OM_NAME}" \
  --context "${K8S_CTX}" -n "${MDB_NS}" \
  --timeout=1800s

# In external-AppDB mode the operator does not manage an internal AppDB, so the
# AppDB part of the status is reported as Disabled (not Running).
echo "Waiting for the primary OM AppDB status to become Disabled (external AppDB is unmanaged by this OM)..."
kubectl wait --for=jsonpath='{.status.applicationDatabase.phase}'=Disabled \
  om/"${PRIMARY_OM_NAME}" \
  --context "${K8S_CTX}" -n "${MDB_NS}" \
  --timeout=600s

echo "[ok] Primary Ops Manager '${PRIMARY_OM_NAME}' is Running and using the external AppDB"
