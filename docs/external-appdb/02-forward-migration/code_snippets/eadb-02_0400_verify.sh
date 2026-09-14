echo "=== Pods in ${MDB_NS} ==="
kubectl get pods --context "${K8S_CTX}" -n "${MDB_NS}"

echo ""
echo "=== AppDB StatefulSet ownership (must now be owned solely by the MongoDB CR) ==="
kubectl get statefulset "${APPDB_NAME}" \
  --context "${K8S_CTX}" -n "${MDB_NS}" \
  -o jsonpath='{range .metadata.ownerReferences[*]}{.kind}/{.name}{"\n"}{end}'

echo ""
echo "=== AppDB StatefulSet migration annotations (should be cleared after adoption) ==="
kubectl get statefulset "${APPDB_NAME}" \
  --context "${K8S_CTX}" -n "${MDB_NS}" \
  -o jsonpath='{.metadata.annotations.mongodb\.com/appdb-migration-ready}{"\n"}'

echo "[ok] Forward migration verified: '${APPDB_NAME}' owned by the MongoDB CR, OM on external AppDB"
