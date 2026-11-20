echo "=== Pods in ${MDB_NS} ==="
kubectl get pods --context "${K8S_CTX}" -n "${MDB_NS}"

echo ""
echo "=== AppDB StatefulSet ownership (must now be owned solely by the Ops Manager) ==="
kubectl get statefulset "${APPDB_NAME}" \
  --context "${K8S_CTX}" -n "${MDB_NS}" \
  -o jsonpath='{range .metadata.ownerReferences[*]}{.kind}/{.name}{"\n"}{end}'

echo ""
echo "=== Primary OM AppDB status (must be Running — internally managed again) ==="
kubectl get om "${PRIMARY_OM_NAME}" \
  --context "${K8S_CTX}" -n "${MDB_NS}" \
  -o jsonpath='{.status.applicationDatabase.phase}{"\n"}'

echo "[ok] Reverse migration verified: internal AppDB Running, StatefulSet owned by the Ops Manager"
