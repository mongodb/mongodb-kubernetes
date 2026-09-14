echo "Waiting for the management Ops Manager's own AppDB to become Running..."
kubectl wait --for=jsonpath='{.status.applicationDatabase.phase}'=Running \
  om/"${MANAGEMENT_OM_NAME}" \
  --context "${K8S_CTX}" -n "${MDB_NS}" \
  --timeout=1200s

echo "Waiting for the management Ops Manager to become Running (pulling the OM image can take a while)..."
kubectl wait --for=jsonpath='{.status.opsManager.phase}'=Running \
  om/"${MANAGEMENT_OM_NAME}" \
  --context "${K8S_CTX}" -n "${MDB_NS}" \
  --timeout=1800s

# opsManager.phase=Running only means the OM pod is up; the public REST API the operator uses to
# create the AppDB's project can lag a few seconds behind. Creating the AppDB CR before the API is
# actually serving can leave its project without an automation config. Gate on the public API
# answering (401 = auth challenge = API layer is up) before proceeding. curl is present in the OM
# container; OM binds to the pod IP (not loopback), so probe that.
echo "Waiting for the management Ops Manager public REST API to start serving..."
om_pod="${MANAGEMENT_OM_NAME}-0"
api_ready=""
for _ in $(seq 1 60); do
  pod_ip=$(kubectl get pod "${om_pod}" --context "${K8S_CTX}" -n "${MDB_NS}" \
    -o jsonpath='{.status.podIP}' 2>/dev/null)
  if [ -n "${pod_ip}" ]; then
    code=$(kubectl exec "${om_pod}" -c mongodb-ops-manager --context "${K8S_CTX}" -n "${MDB_NS}" -- \
      curl -s -o /dev/null -w '%{http_code}' "http://${pod_ip}:8080/api/public/v1.0" 2>/dev/null || true)
    if [ "${code}" = "401" ] || [ "${code}" = "200" ]; then
      echo "[ok] Management OM public REST API is serving (HTTP ${code})"
      api_ready=1
      break
    fi
  fi
  echo "  public API not ready yet (HTTP ${code:-000}), retrying in 10s..."
  sleep 10
done
if [ -z "${api_ready}" ]; then
  echo "ERROR: management OM public REST API did not become ready in time" >&2
  exit 1
fi

echo "[ok] Management Ops Manager '${MANAGEMENT_OM_NAME}' is Running"
echo ""
echo "Its programmatic API-key secret (used as AppDB credentials): ${MANAGEMENT_OM_ADMIN_KEY_SECRET}"
kubectl get secret "${MANAGEMENT_OM_ADMIN_KEY_SECRET}" \
  --context "${K8S_CTX}" -n "${MDB_NS}" -o name
