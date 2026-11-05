echo "Waiting for the AppDB MongoDBMultiCluster resource to become Running..."

kubectl wait --for=jsonpath='{.status.phase}'=Running \
  mdbmc/"${APPDB_NAME}" \
  --context "${K8S_CTX_0}" -n "${MDB_NS}" \
  --timeout=1200s

echo "[ok] AppDB '${APPDB_NAME}' is Running"
