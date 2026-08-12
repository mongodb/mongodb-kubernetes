echo "Waiting for the AppDB MongoDB resource to become Running..."

kubectl wait --for=jsonpath='{.status.phase}'=Running \
  mdb/"${APPDB_NAME}" \
  --context "${K8S_CTX}" -n "${MDB_NS}" \
  --timeout=1200s

echo "[ok] AppDB '${APPDB_NAME}' is Running"
