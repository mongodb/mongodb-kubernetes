echo "Verifying the AppDB CR is blocked on the adoption gate (owned by the primary OM's internal AppDB)..."

# The MongoDB CR must NOT adopt the StatefulSet while it is still owned by the Ops Manager.
kubectl wait --for=jsonpath='{.status.phase}'=Pending \
  mdb/"${APPDB_NAME}" \
  --context "${K8S_CTX}" -n "${MDB_NS}" --timeout=300s

echo "AppDB CR status message:"
kubectl get mdb "${APPDB_NAME}" --context "${K8S_CTX}" -n "${MDB_NS}" \
  -o jsonpath='{.status.message}{"\n"}'

echo "[ok] AppDB '${APPDB_NAME}' is Pending on the adoption gate (as expected before the switch)"
