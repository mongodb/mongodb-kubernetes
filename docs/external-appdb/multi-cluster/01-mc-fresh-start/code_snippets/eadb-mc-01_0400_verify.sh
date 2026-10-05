# In multi-cluster mode the AppDB StatefulSets live in the member clusters (one per cluster, named
# "<appdb>-<clusterIndex>"), and ownership is tracked by the "mongodbmulticluster" LABEL rather than
# ownerReferences. When the external AppDB CR owns them the label equals "<namespace>-<appdb-name>".
expected_owner="${MDB_NS}-${APPDB_NAME}"
member_ctxs=("${K8S_CTX_1}" "${K8S_CTX_2}")

echo "=== AppDB member pods and StatefulSet ownership per member cluster ==="
idx=0
for ctx in "${member_ctxs[@]}"; do
  sts="${APPDB_NAME}-${idx}"
  echo "--- cluster ${ctx} (StatefulSet ${sts}) ---"
  kubectl get pods --context "${ctx}" -n "${MDB_NS}" -l "mongodbmulticluster=${expected_owner}"
  owner=$(kubectl get statefulset "${sts}" --context "${ctx}" -n "${MDB_NS}" \
    -o jsonpath='{.metadata.labels.mongodbmulticluster}' 2>/dev/null)
  echo "  mongodbmulticluster label: ${owner:-<none>} (expected: ${expected_owner})"
  idx=$((idx + 1))
done

echo ""
echo "=== AppDB connection-string secret created by the operator for the primary OM (central cluster) ==="
kubectl get secret "${APPDB_CONNECTION_STRING_SECRET}" \
  --context "${K8S_CTX_0}" -n "${MDB_NS}" -o name

echo ""
echo "[ok] Fresh-start multi-cluster external AppDB verified: primary OM Running against '${APPDB_NAME}'"
