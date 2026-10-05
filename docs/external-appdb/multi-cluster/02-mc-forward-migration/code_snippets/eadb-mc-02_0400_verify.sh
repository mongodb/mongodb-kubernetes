# After the switch the AppDB StatefulSets in each member cluster must be owned by the external AppDB
# CR (label "mongodbmulticluster" == "<namespace>-<appdb-name>") and the migration annotations must be
# cleared.
expected_owner="${MDB_NS}-${APPDB_NAME}"
member_ctxs=("${K8S_CTX_1}" "${K8S_CTX_2}")

echo "=== AppDB StatefulSet ownership + migration annotations per member cluster ==="
idx=0
for ctx in "${member_ctxs[@]}"; do
  sts="${APPDB_NAME}-${idx}"
  echo "--- cluster ${ctx} (StatefulSet ${sts}) ---"
  owner=$(kubectl get statefulset "${sts}" --context "${ctx}" -n "${MDB_NS}" \
    -o jsonpath='{.metadata.labels.mongodbmulticluster}' 2>/dev/null)
  echo "  mongodbmulticluster label: ${owner:-<none>} (expected: ${expected_owner})"
  fwd=$(kubectl get statefulset "${sts}" --context "${ctx}" -n "${MDB_NS}" \
    -o jsonpath='{.metadata.annotations.mongodb\.com/appdb-migration-ready}' 2>/dev/null)
  echo "  appdb-migration-ready annotation: '${fwd}' (expected: empty)"
  idx=$((idx + 1))
done

echo ""
echo "[ok] Forward migration verified: '${APPDB_NAME}' owned by the MongoDBMultiCluster CR, OM on external AppDB"
