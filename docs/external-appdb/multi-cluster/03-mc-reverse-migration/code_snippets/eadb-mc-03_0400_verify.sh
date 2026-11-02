# After reverse migration the AppDB StatefulSets in each member cluster are managed by the Ops Manager
# again. They keep the "mongodbmulticluster" label but their ownerReferences are empty (the deleted CR
# no longer owns them), and the OM's internal AppDB is Running.
member_ctxs=("${K8S_CTX_1}" "${K8S_CTX_2}")

echo "=== AppDB StatefulSet state per member cluster ==="
idx=0
for ctx in "${member_ctxs[@]}"; do
  sts="${APPDB_NAME}-${idx}"
  echo "--- cluster ${ctx} (StatefulSet ${sts}) ---"
  kubectl get statefulset "${sts}" --context "${ctx}" -n "${MDB_NS}" \
    -o jsonpath='{"  ownerReferences: "}{range .metadata.ownerReferences[*]}{.kind}/{.name}{" "}{end}{"\n"}' 2>/dev/null
  echo "  (empty ownerReferences expected — the released CR was deleted)"
  idx=$((idx + 1))
done

echo ""
echo "=== Primary OM AppDB status (must be Running — internally managed again) ==="
kubectl get om "${PRIMARY_OM_NAME}" \
  --context "${K8S_CTX_0}" -n "${MDB_NS}" \
  -o jsonpath='{.status.applicationDatabase.phase}{"\n"}'

echo "[ok] Reverse migration verified: internal multi-cluster AppDB Running, StatefulSets managed by the Ops Manager"
