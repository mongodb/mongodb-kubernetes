echo "Setting the source's search connection parameters (gRPC + TLS + auth)..."
echo "Without these, mongod's search client completes a TLS handshake to the proxy"
echo "WITHOUT offering ALPN h2, Envoy falls back to HTTP/1, and every search call"
echo "hangs ~20s and fails with 'Error connecting to Search Index Management service.'"

# These four parameters are the same for every mongod, so they belong on the CR
# (the operator persists them across reconciles). Only the per-process
# mongotHost values in the next step need the direct automation-config patch.
kubectl patch mdbmc "${RS_RESOURCE_NAME}" -n "${MDB_NAMESPACE}" \
  --context "${K8S_CLUSTER_0_CONTEXT_NAME}" --type merge -p '{
  "spec": {
    "additionalMongodConfig": {
      "setParameter": {
        "useGrpcForSearch": true,
        "searchTLSMode": "requireTLS",
        "skipAuthenticationToMongot": false,
        "skipAuthenticationToSearchIndexManagementServer": false
      }
    }
  }
}'

echo "Waiting for the agents to roll the new parameters onto every mongod..."
applied=0
for _ in $(seq 1 60); do
  applied=$(kubectl exec -n "${MDB_NAMESPACE}" --context "${K8S_CLUSTER_0_CONTEXT_NAME}" \
    "${RS_RESOURCE_NAME}-0-0" -c mongodb-enterprise-database -- \
    grep -c "useGrpcForSearch" /data/automation-mongod.conf 2>/dev/null || true)
  [[ "${applied}" == "1" ]] && break
  sleep 5
done
if [[ "${applied}" != "1" ]]; then
  echo "Error: mongod's config never picked up useGrpcForSearch within 5 minutes -- stop here." >&2
  echo "Check the MongoDBMultiCluster status and the automation agent logs." >&2
  exit 1
fi

echo "Waiting for the resource to get back to the Running phase..."
kubectl wait --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" \
  --for=jsonpath='{.status.phase}'=Running "mdbmc/${RS_RESOURCE_NAME}" --timeout=600s
echo "[ok] search connection parameters applied on every mongod"
