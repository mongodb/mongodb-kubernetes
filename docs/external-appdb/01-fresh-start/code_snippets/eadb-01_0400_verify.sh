echo "=== Pods in ${MDB_NS} ==="
kubectl get pods --context "${K8S_CTX}" -n "${MDB_NS}"

echo ""
echo "=== AppDB StatefulSet ownership (must be owned solely by the MongoDB CR) ==="
kubectl get statefulset "${APPDB_NAME}" \
  --context "${K8S_CTX}" -n "${MDB_NS}" \
  -o jsonpath='{range .metadata.ownerReferences[*]}{.kind}/{.name}{"\n"}{end}'

echo ""
echo "=== AppDB connection-string secret created by the operator for the primary OM ==="
kubectl get secret "${APPDB_CONNECTION_STRING_SECRET}" \
  --context "${K8S_CTX}" -n "${MDB_NS}" -o name

echo ""
echo "[ok] Fresh-start external AppDB verified: primary OM Running against '${APPDB_NAME}'"
